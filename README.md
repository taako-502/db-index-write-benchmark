# mongodb-index-write-benchmark

MongoDB・PostgreSQLの追加インデックス数と更新対象による書き込み性能の違いを、Go 1.27で測定します。結果はJSON・CSVと、オフラインで開けるHTMLグラフに出力します。

## 画面から設定・実行

```sh
docker compose up -d --wait
go run ./cmd/bench -serve
open http://127.0.0.1:8080
```

DB（片方・両方）、DBの実行順、データ件数、追加本数、反復数、バッチ件数、seed、永続化待ち、PostgreSQLのfillfactor、待機時間、タイムアウトを設定できます。同時に実行できる計測は1つです。進捗は成功した試行数で、ウォームアップや初期投入中は進みません。「中断」は実行をキャンセルし、専用DB/スキーマを削除します。成功済みの結果は残ります。

画面は `127.0.0.1:8080` でのみ待ち受けます（`-listen` は数値ループバックアドレスのみ）。出力は `results/<実行ID>/`、`-out` でルートを指定できます。画面の履歴はサーバー起動中の実行を表示します。再起動後も保存済みHTMLを直接開けます。接続先のURIや認証情報をブラウザから変更する機能はありません。

## 両DBの比較

```sh
go run ./cmd/bench -engines mongodb,postgresql -sizes 10000 -repeats 3 -out results/compare
open results/compare/report.html
# 本計測。必要に応じて別実行でDB順序を逆転してください。
go run ./cmd/bench -engines postgresql,mongodb -sizes 100000,1000000 -repeats 5 -out results/full-pg-first
```

両DBは順次実行し、各DB内の条件順は同じseedでシャッフルします。DBの実行順はランダム化しません。時間経過による温度や負荷の偏りを確認するには、順序を逆転した別実行を比較してください。

| 条件 | MongoDB | PostgreSQL |
|---|---|---|
| データ | int64の同じ7フィールド | bigintの同じ7列（`_id` → `id`） |
| 基準 | `_id`インデックス | `id`のPRIMARY KEY |
| 追加 | 0〜5本の単一フィールドインデックス | 0〜5本のB-tree単一列インデックス |
| insert | InsertMany（ordered） | pgx SendBatch、1行1 INSERT |
| update | BulkWrite（ordered）、`_id_` hint | pgx SendBatch、1行1 UPDATE、主キーで検索 |
| バッチ境界 | 複数ドキュメントのトランザクションなし | 各バッチが暗黙の1トランザクション |
| 永続化待ちなし | w:1, j:false | synchronous_commit=off |
| 永続化待ちあり | w:1, j:true | synchronous_commit=on |
| 初期状態 | 各試行で新規コレクション | 各試行で新規テーブル |

**主指標は同じDB・件数・操作の追加0本に対する時間倍率です。** HTMLのDB選択で片方・両方を表示できます。両DB表示では各DBの0本をそれぞれ分母にし、濃色がMongoDB、淡色がPostgreSQLです。JSON/CSVの各行にもDB名を保存します。旧schema_version=1のMongoDB結果も `-report` で再生成できます。

原子性、通信方式、エンコード、バッチ内のSQL実行回数、ストレージ構造は異なります。絶対的な件数/秒はこれらを含むエンドツーエンドの測定です。トランザクション保証やキャッシュ容量が完全に同じとは解釈しないでください。COPYや複数VALUES文は今回使いません。PostgreSQLではバッチ送信から全応答とcommit応答を取得するCloseまでを測定します。バッチの準備・BSONからの共通データ変換は除外します。

PostgreSQLは **17.6**、CPU 2、メモリ3GB、shared_buffers=1GB、ホストポート5433です。MongoDBのWiredTigerキャッシュ1GBとshared_buffers=1GBは同じ意味のメモリ上限ではありません。pgxはv5.7.6を固定します。`POSTGRES_URI` の既定値は `postgres://benchmark:benchmark@127.0.0.1:5433/benchmark?sslmode=disable`（ローカル検証専用）です。既存DBにランダム名の専用スキーマを作成し、そのスキーマだけを削除します。接続ユーザーにはスキーマ作成権限が必要です。

PostgreSQLは初期投入後にANALYZEを行い、セッション内で `enable_seqscan=off` にして主キー検索を優先します。強制hintではありません。fillfactorは既定100、autovacuumは有効です。これらとserver_version、shared_buffers、synchronous_commit、fsync、full_page_writes、wal_levelを結果に記録します。接続先URIは記録しません。

**PostgreSQLの物理的なインデックス更新数は、変更した列の本数とは一致しません。** HOT更新の可否は対象列がインデックスに含まれるかに加え、元のページの空き容量にも依存します。fillfactorを変更した実験は別条件として保存してください。MVCCによる新しい行バージョンやVACUUMもMongoDBとの違いに含まれます。

参考： [PostgreSQL HOT](https://www.postgresql.org/docs/17/storage-hot.html)、[synchronous_commit](https://www.postgresql.org/docs/17/runtime-config-wal.html#GUC-SYNCHRONOUS-COMMIT)、[pgx SendBatch](https://pkg.go.dev/github.com/jackc/pgx/v5#Conn.SendBatch)。

## 起動と実行

必要なもの：Go 1.27.0以降、Docker Compose。

```sh
docker compose up -d --wait
go run ./cmd/bench -sizes 10000 -repeats 2 -out results/quick
open results/quick/report.html  # macOS。ほかのOSではブラウザで開く
```

本計測（2データ件数 × 3インデックス条件 × 4操作 × 5試行 = 120試行）：

```sh
go run ./cmd/bench \
  -sizes 100000,1000000 \
  -indexes 0,1,5 \
  -repeats 5 \
  -batch 1000 \
  -seed 42 \
  -notes 'MacBook Pro / メモリ24GB / Docker: CPU 2, RAM 3GB / SSD' \
  -out results/full
open results/full/report.html
```

`-notes`は実際の環境に書き換えてください。実行時間は環境に依存し、100万件を各条件で初期投入し直すため時間がかかります。最初に小規模で動作確認してください。

```sh
docker compose down  # 検証用コンテナの停止。ボリュームは保持
```

ComposeはMongoDB **8.0.16**、CPU 2、メモリ3GB、WiredTigerキャッシュ1GBで固定し、ホストの`127.0.0.1:27018`に公開します。既存のローカルMongoDBと衝突しにくいポートを使っています。Goドライバはv2.5.0を固定しています。

## MongoDBで何を比較するか

全条件で`_id`インデックスは存在します。「0本」は**追加インデックス0本**です。

| 追加本数 | 定義 |
|---|---|
| 0 | `_id`のみ |
| 1 | `_id` + `{field1: 1}` |
| 5 | `_id` + `field1`〜`field5`それぞれの単一フィールドインデックス |

| 操作 | 内容 | 更新される追加インデックス数（0本 / 1本 / 5本条件） |
|---|---|---|
| `insert` | 空のコレクションへN件を投入 | 0 / 1 / 5 |
| `update_unindexed` | `counter`を`$inc: 1` | 0 / 0 / 0 |
| `update_one` | `field1`を`$inc: 1` | 0 / 1 / 1 |
| `update_five` | `field1`〜`field5`をすべて`$inc: 1` | 0 / 1 / 5 |

updateでは毎回N件を初期投入し、全ドキュメントを1回ずつ更新します。検索は`_id`の完全一致、`hint: "_id_"`で固定します。`$inc`で値を実際に変更し、各バッチの`ModifiedCount`も確認します。複合・unique・sparse・multikeyインデックスは今回の対象外です。

ドキュメントは`_id`、`counter`、`field1`〜`field5`のint64値です。`_id`は連番、追加フィールドは固定seedで生成する0〜999,999,999の疑似乱数です。更新対象の順序も固定seedでシャッフルし、全条件・全試行で同じ順序にします。ドキュメントのフィールド数と数値型は更新前後で変わりません。

## 共通の計測方法（以下のDB固有表記はMongoDB）

1. 実行ごとにランダムな名前の専用DBを作成。
2. 各操作・インデックス条件を最大1,000件でウォームアップ（集計対象外）。
3. 各試行でコレクションを再作成し、インデックスを作成。updateの場合は初期データを投入。
4. 設定した待機時間（既定1秒）を置き、Go側でGCを実施。
5. 同時実行数1でバッチ書き込みを実施。
6. 件数を検証し、結果を保存。次の条件へ進む。
7. 通常終了・エラー・Ctrl+C時に、その実行が作成した専用DBを削除。

各ラウンドで全条件を1回ずつ実施し、条件の順番をseedに基づいてシャッフルします。`w:1, j:false`が既定で、書き込み・読み込みの自動リトライは無効です。ジャーナルへの永続化を待つ比較は別実行で`-journal`を指定してください。`j:false`はジャーナリング自体の無効化を意味しません。

**主指標は「各バッチのドライバ呼び出し開始から応答まで」の合計時間です。** データ生成・初期BSON生成・更新モデルの準備・接続・インデックス作成・初期投入・結果検証・レポート生成は含みません。ドライバ内部の処理、更新コマンドのBSONエンコード、ネットワーク往復、サーバー処理、呼び出し中に発生したGCは含みます。サーバー内部だけの処理時間ではありません。

別途、バッチ準備とループ処理を含む書き込みフェーズ全体の壁時計時間（`wall_seconds`）も保存します。

- insertは`InsertMany`、updateは`BulkWrite`（ordered=true）。
- `-batch 1`も1件入りの同じAPIを使用します。`InsertOne`/`UpdateOne`の専用ベンチマークではありません。
- 更新は初期投入直後なので、キャッシュが温まった条件です。OS・WiredTigerキャッシュの消去はしていません。
- 事前生成したデータをクライアントメモリに保持します。データ件数に比例してクライアントのメモリも増えます。
- デフォルトデータは小さく、データ件数を増やしても必ずキャッシュ超過を再現するわけではありません。
- 一括投入の速度を測るため、同じ件数の範囲でもインデックスは投入中に成長します。一定サイズの既存コレクションに追記する実験とは条件が異なります。
- 他の負荷、CPU温度、ディスク、Docker VMのリソース、チェックポイント等の影響は残ります。条件順のシャッフルや5回の反復で消えるとは限りません。

## グラフと出力ファイル

| ファイル | 内容 |
|---|---|
| `results.json` | 設定・環境・全試行の実測値・完了状態 |
| `results.csv` | 各試行の時間、件数/秒、インデックス容量 |
| `report.html` | データ件数・表示指標を切り替えるグラフと集計表 |

HTMLにはデータと描画処理を埋め込み、CDNやWebサーバーを使いません。各グラフの「SVG保存」で記事用の画像を保存できます。JSON/CSVへのリンクも使う場合は、3ファイルを同じフォルダに置いてください。

- 棒：中央値、線：最小〜最大、点：各試行。
- スループットは各試行で`N / driver_seconds`を求めてから中央値を計算。
- 時間倍率は「対象条件の時間中央値 / 同じ件数・同じ操作の0本条件の時間中央値」。倍率の点・範囲も0本の時間中央値を固定分母に使用。
- 最小〜最大は信頼区間ではありません。
- 容量は測定後の`collStats.totalIndexSize`（`_id`を含む）。物理容量の補助情報です。

成功した試行ごとにファイルを更新します。中断・失敗した試行は集計せず、それ以前の結果を`completed:false`として残します。初期接続前のエラーでは結果ファイルが作られません。途中結果はHTMLでも明示します。出力先に既存ディレクトリを指定した場合はエラーにし、過去の結果を上書きしません。

MongoDBに接続せず、保存済みJSONからHTML/CSVを再生成できます。

```sh
go run ./cmd/bench -report results/full/results.json -out results/regenerated
```

## オプション

```sh
go run ./cmd/bench -help
```

| オプション | 既定値 | 内容 |
|---|---|---|
| `-engines` | `mongodb` | `mongodb`、`postgresql`、または両方をカンマ区切りで実行順に指定 |
| `-pg-fillfactor` | `100` | PostgreSQLテーブルのfillfactor（10〜100） |
| `-serve` | `false` | ローカルの設定・実行画面を起動 |
| `-listen` | `127.0.0.1:8080` | 画面の待ち受け先 |
| `-sizes` | `100000,1000000` | 各試行のドキュメント数 |
| `-indexes` | `0,1,5` | 追加本数。0〜5で0を含める |
| `-repeats` | `5` | 各条件の測定回数 |
| `-batch` | `1000` | バッチ件数（1〜100000） |
| `-seed` | `42` | データ・更新順・条件順のseed |
| `-journal` | `false` | ジャーナル応答待ち |
| `-timeout` | `2m` | 各DB操作のタイムアウト |
| `-cooldown` | `1s` | セットアップ後の待機 |
| `-notes` | 空 | マシン・ディスク・Docker設定など |
| `-out` | `results/<UTC時刻>` | 新規出力ディレクトリ |
| `-report` | 空 | 再生成元のJSONパス |

接続先は環境変数`MONGODB_URI`（既定`mongodb://127.0.0.1:27018`）。URIは結果へ保存しません。接続ユーザーには専用DBの作成・書き込み・インデックス作成・統計参照・削除が必要です。プログラムは既存DB名を受け取らず、`index_write_bench_<ランダム文字列>`だけを使用します。強制終了・接続断では後片付けできないことがあるため、ログに作成したDB名を表示します。

## テスト

```sh
go test ./...
go vet ./...
# Docker起動後：実際のMongoDBで全操作・全インデックス条件を確認
BENCH_TEST_URI=mongodb://127.0.0.1:27018 \
BENCH_TEST_POSTGRES_URI='postgres://benchmark:benchmark@127.0.0.1:5433/benchmark?sslmode=disable' \
go test -race ./...
```

単体テストは条件網羅・再現性・集計・入力検証・HTMLへのJSON埋め込みを確認します。結合テストは23件を7件単位で書き込み、端数バッチも検証します。GitHub ActionsではMongoDBを起動して結合テストとCLIの実行、レポート再生成を行います。CIの小規模な実行は動作確認用で、性能の結論には使用しません。

参考：[MongoDB Write Operation Performance](https://www.mongodb.com/docs/manual/core/write-performance/)

PostgreSQLの結合テストは全操作・全インデックス条件で全行の更新値とスキーマ削除を検証します。HTTPテストは設定検証・同時実行拒否・中断と途中保存・Origin/Host検証を確認します。CIは両DBの結合テスト・比較レポート生成を実施します。
