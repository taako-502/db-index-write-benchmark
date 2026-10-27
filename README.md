# MongoDB・PostgreSQLのインデックス書き込み性能ベンチマーク

**インデックスを増やすと、挿入や更新にどれだけ時間がかかるか**を測るツールです。MongoDBとPostgreSQLで、追加インデックスの本数と更新するフィールドを変えて計測し、結果をJSON・CSV・HTMLグラフに保存します。Go 1.27で動作します。

主に見るのは、**同じDB・データ件数・操作で、追加インデックス0本に対して処理時間が何倍になったか**です。両DBは書き込みAPIやトランザクションの仕組みが異なるため、DB間の速度差を読む際は[比較条件](#比較条件とdbごとの違い)も確認してください。

さらに、**1万・10万・100万件とデータ件数を増やし、追加インデックスによる時間倍率や件数/秒がどう変わるか**を比較できます。最初の1万件は動作確認用で、件数による影響は[件数を増やして比較する手順](#データ件数を増やして影響を比較する)で確認します。

## まず小規模で試す

必要なものはGo 1.27以降とDocker Composeです。リポジトリのルートで次を実行します。

```sh
docker compose up -d --wait
go run ./cmd/bench -engines mongodb,postgresql -sizes 10000 -repeats 3 -out results/quick
```

MongoDB → PostgreSQLの順に、1万件・追加インデックス0/1/5本・4種類の操作を各3回測定します。ウォームアップを除き、合計72試行です。

完了したら、`results/quick/report.html`をブラウザで開きます。macOSでは次のコマンドを使えます。

```sh
open results/quick/report.html
```

出力先は毎回新しいディレクトリを指定してください。同じコマンドを再実行する場合は、`results/quick-2`などに変更します。

終了後はコンテナを停止できます。ボリュームは保持されます。

```sh
docker compose down
```

## 結果の読み方

### 最初に見る指標

HTMLでデータ件数とDBを選び、「DBごとの0本に対する所要時間倍率」を確認します。

- **1倍**：追加インデックス0本と同じ時間。
- **2倍**：同じ件数の書き込みに、0本条件の2倍の時間がかかった。
- **1倍未満**：その実行では0本条件より短い時間だった。試行ごとのばらつきも確認する。

「0本」は追加インデックスの本数です。MongoDBの`_id`インデックス、PostgreSQLの`id`のPRIMARY KEYは常に存在します。両DBを表示しても、倍率の分母は各DB自身の0本条件です。

棒は中央値、線は最小〜最大、点は各試行を表します。最小〜最大は信頼区間ではありません。両DB表示では濃色がMongoDB、淡色がPostgreSQLです。「スループット（件/秒）」や「処理時間（秒）」にも切り替えられます。

倍率は「対象条件の時間中央値 ÷ 同じDB・件数・操作の0本条件の時間中央値」です。倍率の点・範囲にも、この固定分母を使います。スループットは各試行の「件数 ÷ ドライバ呼び出し時間」を求めてから中央値を計算します。

### 出力ファイル

| ファイル | 内容 |
|---|---|
| `results.json` | 設定・環境・全試行の実測値・完了状態 |
| `results.csv` | 各試行の時間、件数/秒、インデックス容量 |
| `report.html` | データ件数・DB・表示指標を切り替えるグラフと集計表 |

HTMLはデータと描画処理を内包するため、オフラインで開けます。各グラフの「SVG保存」で画像を保存できます。JSON/CSVへのリンクも使う場合は、3ファイルを同じフォルダに置いてください。

インデックス容量は測定後の補助情報です。MongoDBは`collStats.totalIndexSize`（`_id`を含む）、PostgreSQLは`pg_indexes_size`（PRIMARY KEYを含む）を保存します。

### 中断した場合・レポートを再生成する場合

成功した試行ごとにファイルを更新します。中断・失敗した試行は集計せず、それ以前の結果を`completed:false`として残します。途中結果であることはHTMLにも表示します。0本条件が未計測のDBは倍率を表示できません。

保存済みJSONから、DBに接続せずHTML/CSVを再生成できます。旧`schema_version=1`のMongoDB結果にも対応します。

```sh
go run ./cmd/bench -report results/quick/results.json -out results/regenerated
```

## 実行方法を変える

いずれも、計測対象のDBを起動してから実行してください。

### ブラウザで条件を設定する

```sh
docker compose up -d --wait
go run ./cmd/bench -serve
```

`http://127.0.0.1:8080`をブラウザで開きます。初期設定は両DB・1万件・追加インデックス0/1/5本・3回反復です。「計測開始」を押すと実行します。

DBと実行順、データ件数、追加本数、反復数、バッチ件数、乱数seed、永続化応答の待機、PostgreSQLのfillfactor、セットアップ後の待機時間、タイムアウト、環境メモを変更できます。

同時に実行できる計測は1つです。進捗は成功した試行数で、ウォームアップや初期投入中は進みません。「中断」は実行をキャンセルし、その実行の専用DB/スキーマを削除します。成功済みの結果は残ります。

出力は`results/<実行ID>/`です。`-out`で出力ルートを変更できます。履歴に表示するのはサーバー起動中の実行で、再起動後も保存済みHTMLを直接開けます。

画面は既定で`127.0.0.1:8080`で待ち受け、`-listen`には数値ループバックアドレスのみ指定できます。接続先URIや認証情報は、サーバー起動時の環境変数で設定します。

### CLIで片方のDBだけ測定する

```sh
# MongoDBのみ（-enginesを省略した場合も同じ）
go run ./cmd/bench -engines mongodb -sizes 10000 -repeats 3 -out results/mongo

# PostgreSQLのみ
go run ./cmd/bench -engines postgresql -sizes 10000 -repeats 3 -out results/postgres
```

### データ件数を増やして影響を比較する

小規模で動作確認した後、1万・10万・100万件を同じインデックス条件・バッチ件数・反復数で測定します。次の例は、2 DB × 3データ件数 × 3インデックス条件 × 4操作 × 5試行 = 360試行です。

```sh
go run ./cmd/bench \
  -engines mongodb,postgresql \
  -sizes 10000,100000,1000000 \
  -indexes 0,1,5 \
  -repeats 5 \
  -batch 1000 \
  -seed 42 \
  -notes 'MacBook Pro / メモリ24GB / 各DBコンテナ: CPU 2, RAM 3GB / SSD' \
  -out results/full
```

`-notes`は実際の環境に書き換えてください。各試行でコレクション/テーブルを作り直し、更新試行では初期データも投入し直すため、実行時間は長くなります。

HTMLの「データ件数」を切り替え、同じDB・操作・追加本数について、次を比較します。

- **時間倍率**：件数が増えると、追加0本に対するインデックスの負担が大きくなるか。
- **件数/秒**：件数が増えても書き込み速度を維持できるか。
- **処理時間**：データ量に応じて総時間がどれだけ増えるか。件数そのものが増えるため、倍率や件数/秒と合わせて読む。

例えば、5本条件の倍率が1万件では1.2倍、100万件では2倍なら、その測定では件数の増加とともに追加インデックスの相対的な負担が大きくなっています（数値は説明用です）。件数の増加だけでキャッシュ超過やディスクI/Oが原因だと断定することはできません。

DBの実行順は指定した順で固定です。時間経過による温度や負荷の偏りを確認するには、`-engines postgresql,mongodb`として別の出力先で実行し、結果を比較してください。

## 比較条件とDBごとの違い

### 共通のデータと操作

各データは`_id`（PostgreSQLでは`id`）、`counter`、`field1`〜`field5`の7項目です。MongoDBはint64、PostgreSQLはbigintを使います。IDは連番、`counter`の初期値は0、`field1`〜`field5`は固定seedで生成する0〜999,999,999の疑似乱数です。

| 追加インデックス本数 | 対象 |
|---|---|
| 0 | IDのインデックスのみ |
| 1 | IDに加えて`field1` |
| 5 | IDに加えて`field1`〜`field5`それぞれの単一フィールド/列インデックス |

既定の比較条件は0/1/5本です。`-indexes`で0〜5本の範囲を指定でき、基準となる0本を必ず含めます。複合・unique（ID以外）・sparse・multikeyインデックスは対象外です。

| 操作 | 内容 | 更新対象に含まれる追加インデックス数（0本 / 1本 / 5本条件） |
|---|---|---|
| `insert` | 空のコレクション/テーブルへN件を投入 | 0 / 1 / 5 |
| `update_unindexed` | `counter`に1を加算 | 0 / 0 / 0 |
| `update_one` | `field1`に1を加算 | 0 / 1 / 1 |
| `update_five` | `field1`〜`field5`すべてに1を加算 | 0 / 1 / 5 |

更新試行では毎回N件を初期投入し、IDの完全一致で全件を1回ずつ更新します。更新順は固定seedでシャッフルし、同じデータ件数では全条件・全試行で同じ順序を使います。フィールド数と数値型は更新前後で変わりません。

**表の本数は、DB内部で物理的に更新されるインデックス数を保証するものではありません。** 特にPostgreSQLでは、後述するHOT更新やMVCCの影響があります。

### 書き込み方式の違い

| 条件 | MongoDB | PostgreSQL |
|---|---|---|
| 基準となるインデックス | `_id`インデックス | `id`のPRIMARY KEY |
| 追加インデックス | 単一フィールドインデックス | B-tree単一列インデックス |
| 挿入API | InsertMany（ordered） | pgx SendBatch、1行1 INSERT |
| 更新API | BulkWrite（ordered）、`_id_` hint | pgx SendBatch、1行1 UPDATE、主キーで検索 |
| バッチ境界 | 複数ドキュメントのトランザクションなし | 各バッチが暗黙の1トランザクション |
| 永続化応答を待たない（既定） | w:1, j:false | synchronous_commit=off |
| 永続化応答を待つ（`-journal`） | w:1, j:true | synchronous_commit=on |
| 各試行の初期状態 | 新規コレクション | 新規テーブル |

原子性、通信方式、エンコード、バッチ内のSQL実行回数、ストレージ構造は異なります。件数/秒はこれらを含むエンドツーエンドの測定です。トランザクション保証やキャッシュ容量が完全に同じとは解釈しないでください。PostgreSQLのCOPYや複数VALUES文は使いません。

MongoDBの書き込み・読み込みの自動リトライは無効です。`j:false`はジャーナリング自体の無効化を意味しません。永続化応答の待機を比較する場合は、`-journal`を付けた別実行として保存してください。

### Docker Composeの実行環境

| 設定 | MongoDB | PostgreSQL |
|---|---|---|
| サーバーバージョン | 8.0.16 | 17.6 |
| 各コンテナのCPU / メモリ上限 | 2 CPU / 3GB | 2 CPU / 3GB |
| DBのキャッシュ設定 | WiredTigerキャッシュ1GB | shared_buffers=1GB |
| ホストの接続先 | `127.0.0.1:27018` | `127.0.0.1:5433` |

WiredTigerキャッシュとshared_buffersは、同じ意味のメモリ上限ではありません。Goドライバーのバージョンは[go.mod](go.mod)、コンテナの設定は[compose.yaml](compose.yaml)で管理します。実際に使ったDB・ドライバーのバージョンは結果にも記録します。

### PostgreSQLの更新特性

更新試行では初期投入後にANALYZEを実行します。セッション内で`enable_seqscan=off`として主キー検索を優先しますが、強制hintではありません。fillfactorは既定100で、autovacuumの設定は変更しません。

HOT更新の可否は、更新する列がインデックスに含まれるかに加え、元のページの空き容量にも依存します。MVCCによる新しい行バージョンやVACUUMもMongoDBとの違いに含まれます。fillfactorを変えた実験は、別条件として保存してください。

fillfactor、server_version、shared_buffers、synchronous_commit、fsync、full_page_writes、wal_level、autovacuumを結果に記録します。

## 計測方法と測定範囲

### 試行の流れ（両DB共通）

1. 実行ごとに専用の領域を用意する。MongoDBはランダム名のDB、PostgreSQLは接続先DB内のランダム名のスキーマを使う。
2. 各操作・インデックス条件を最大1,000件でウォームアップする（集計対象外）。
3. 各試行でコレクション/テーブルとインデックスを作り直す。更新試行では初期データを投入する。
4. 設定した待機時間（既定1秒）を置き、Go側でGCを実施する。
5. 同時実行数1でバッチ書き込みを行う。
6. 件数を検証して結果を保存し、次の条件へ進む。
7. 通常終了・エラー・Ctrl+C時に、その実行の専用DB/スキーマを削除する。

各ラウンドで全条件を1回ずつ実施し、条件の順番をseedに基づいてシャッフルします。両DBは順次実行し、各DB内では同じseedによる条件順を使います。

### 計測時間に含むもの

**処理時間は、各バッチのドライバー呼び出し開始から応答までの合計です。** JSONでは`seconds`、CSVでは`driver_seconds`として保存します。

| 含む処理 | 含まない処理 |
|---|---|
| ドライバー内部の処理・エンコード、ネットワーク往復、サーバー処理、呼び出し中のGC | データ生成・初期BSON生成・共通データ変換、バッチ/更新モデルの準備、接続、インデックス作成、更新前の初期投入、結果検証、レポート生成 |

MongoDBはInsertMany/BulkWriteの応答まで、PostgreSQLはSendBatchから全SQLの応答とcommit応答を取得するCloseまでを測定します。サーバー内部だけの処理時間ではありません。

別途、バッチ準備とループ処理を含む書き込みフェーズ全体の経過時間を`wall_seconds`として保存します。

### 解釈上の注意

- `-batch 1`でも同じバッチAPIを使います。MongoDBのInsertOne/UpdateOneの専用ベンチマークではありません。
- 更新は初期投入直後の、キャッシュが温まった条件です。DB・OSキャッシュを消去しません。
- 事前生成したデータをクライアントメモリに保持するため、件数に比例してクライアントのメモリも増えます。
- データは小さく、件数を増やしても必ずキャッシュ超過を再現するわけではありません。
- 挿入中にインデックスが成長します。一定サイズの既存データに追記する実験とは条件が異なります。
- 他の負荷、CPU温度、ディスク、Docker VMのリソース、チェックポイント等の影響は残ります。条件順のシャッフルや5回の反復で消えるとは限りません。

## CLIオプションと接続設定

```sh
go run ./cmd/bench -help
```

| オプション | 既定値 | 内容 |
|---|---|---|
| `-engines` | `mongodb` | `mongodb`、`postgresql`、または両方を実行順にカンマ区切りで指定 |
| `-sizes` | `100000,1000000` | 各試行のデータ件数 |
| `-indexes` | `0,1,5` | 追加インデックス本数。0〜5で0を含める |
| `-repeats` | `5` | 各条件の測定回数 |
| `-batch` | `1000` | バッチ件数（1〜100000） |
| `-seed` | `42` | データ・更新順・条件順の乱数seed |
| `-journal` | `false` | 永続化応答を待つ（MongoDB j:true / PostgreSQL synchronous_commit=on） |
| `-pg-fillfactor` | `100` | PostgreSQLテーブルのfillfactor（10〜100） |
| `-timeout` | `2m` | 各DB操作・セットアップ呼び出しのタイムアウト |
| `-cooldown` | `1s` | 各試行のセットアップ後の待機時間 |
| `-notes` | 空 | マシン・ディスク・Docker設定などの環境メモ |
| `-out` | `results/<UTC時刻>` | 新規出力ディレクトリ。`-serve`では出力ルート（既定`results`） |
| `-report` | 空 | 再生成元のJSONパス |
| `-serve` | `false` | ローカルの設定・実行画面を起動 |
| `-listen` | `127.0.0.1:8080` | 画面の待ち受け先 |

上記はCLIの既定値です。ブラウザ画面は、小規模で試せる初期設定を使います。

### 接続先と作成・削除する領域

| 環境変数 | 既定値 |
|---|---|
| `MONGODB_URI` | `mongodb://127.0.0.1:27018` |
| `POSTGRES_URI` | `postgres://benchmark:benchmark@127.0.0.1:5433/benchmark?sslmode=disable` |

既定のURIはDocker Composeによるローカル検証用です。URIや認証情報は結果へ保存しません。

MongoDBは`index_write_bench_<ランダム文字列>`という専用DBを作成・削除します。接続ユーザーには専用DBの作成・書き込み・インデックス作成・統計参照・削除と、サーバーバージョン取得に必要な権限が必要です。

PostgreSQLは接続先DB内に同様の名前の専用スキーマを作成し、そのスキーマを削除します。接続先DBは事前に用意し、接続ユーザーにはスキーマ作成とその中でのテーブル・インデックス作成、読み書き・削除に必要な権限を与えてください。

強制終了・接続断では後片付けできないことがあります。作成したDB/スキーマ名はログに表示します。

## テストと検証

```sh
go test ./...
go vet ./...
```

DB結合テストは、接続先の環境変数を設定した場合に実行します。

```sh
docker compose up -d --wait
BENCH_TEST_URI=mongodb://127.0.0.1:27018 \
BENCH_TEST_POSTGRES_URI='postgres://benchmark:benchmark@127.0.0.1:5433/benchmark?sslmode=disable' \
go test -race ./...
```

単体テストでは条件網羅・再現性・集計・入力検証・HTMLへのJSON埋め込みを確認します。両DBの結合テストは23件を7件単位で書き込み、全操作・全インデックス条件と端数バッチを検証します。PostgreSQLでは全行の更新値とスキーマ削除も検証します。HTTPの単体テストは設定検証・同時実行拒否・中断と途中保存・Origin/Host検証を確認します。

GitHub Actionsは`go.mod`のGoバージョンを使い、両DBの結合テスト、CLIでの比較レポート生成、実際のHTTPサーバーを使った設定・実行・中断、保存済みJSONからのレポート再生成を確認します。CIの小規模な実行は動作確認用で、性能の結論には使用しません。

## ブログ記事
https://ap-ep.com/db-index-write-overhead/

## 参考資料

- [MongoDB Write Operation Performance](https://www.mongodb.com/docs/manual/core/write-performance/)
- [PostgreSQL HOT](https://www.postgresql.org/docs/17/storage-hot.html)
- [PostgreSQL synchronous_commit](https://www.postgresql.org/docs/17/runtime-config-wal.html#GUC-SYNCHRONOUS-COMMIT)
- [pgx SendBatch](https://pkg.go.dev/github.com/jackc/pgx/v5#Conn.SendBatch)
