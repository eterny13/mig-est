# mig-est 仕様書 v0.1（ドラフト）

- プロジェクト仮称: mig-est
- 対象データベース（プロトタイプ）: MySQL 8.0 / 8.4（InnoDB）
- 作成日: 2026-10-30
- ステータス: ドラフト。実装開始前の設計文書で、実装中に更新する。
- 一文要約: マイグレーションSQLを本番に流す前に、「どのアルゴリズムで実行されるか」「何をブロックするか」「どれくらい時間がかかりそうか」「ディスクは足りるか」「安全な代替案は何か」を、実行せずに診断するCLIツール。

---

## 0. 決定事項（この仕様書の前提）

| 項目 | 決定 |
|---|---|
| 対象DB（v0.1） | MySQL 8.0 / 8.4、InnoDBのみ。MariaDB、MySQL 5.7、Postgresは対象外 |
| 実装言語 | Go（理由は第16章） |
| 形態 | 単体バイナリのCLI。サーバー、デーモン、SaaSは作らない |
| 基本方針 | 診断のみ。マイグレーション自体は絶対に実行しない |
| 接続方針 | 読み取り専用ユーザーで接続する。権限が取れない場合はサイズ手入力のオフラインモードで動く |
| 最初の配布 | GitHub ReleasesでLinux / macOSのバイナリ配布。Homebrewはv0.2以降で検討 |
| ライセンス | MITを推奨（公開時に最終決定） |

---

## 1. 背景と課題

MySQLの `ALTER TABLE` は、見た目が1行でも中身が大きく違う。

- メタデータだけ変わってミリ秒で終わるもの
- テーブル全体をコピーし直して数十分〜数時間かかるもの
- 書き込みは止めないが、最後に短いメタデータロック（MDL）待ちが発生するもの
- 長時間トランザクションが残っているせいで、ALTER自体が開始すらできないもの

現場で起きる事故の多くは「遅いALTER」そのものより、「MDL待ちで後続のクエリが詰まる」「COPYなのに気づかず昼間に流す」「ディスクが足りずに途中で失敗する」といった、事前に調べれば防げたものだ。

既存ツールとの位置づけは次の通り。

| ツール | 役割 | mig-estとの違い |
|---|---|---|
| Flyway / Alembic / goose など | マイグレーションの実行・管理 | 実行はしない。流す前の診断に特化する |
| gh-ost / pt-online-schema-change | オンラインで安全に実行する | 実行はしない。そもそもネイティブDDLで済むか、gh-ost等が必要かを判定する側 |
| MySQL公式ドキュメントのOnline DDL表 | アルゴリズムの仕様 | 人間が毎回表を引く代わりに、SQLと実テーブル情報から自動判定する |

つまり mig-est は「terraform plan の MySQLマイグレーション版」であり、CIでマイグレーションのPRを止められるチェックツールである。

---

## 2. 目的と非目的

### 2.1 目的

1. マイグレーションSQLから、MySQLが選択する実行アルゴリズム（INSTANT / INPLACE / COPY）を予測する。
2. 対象テーブルの実サイズ・行数・インデックス・外部キー・トリガー・パーティションを踏まえて危険度を判定する。
3. MDLブロッカー（長時間トランザクション、待機中のロック）、レプリケーション遅延、ディスク不足を事前に検出する。
4. 時間を「単一の数字」ではなく「範囲＋信頼度」で見積もり、較正データで徐々に精度を上げる。
5. 危険な操作に対して、具体的な安全な代替SQLや手順を提示する。
6. CIで使える終了コードとJSON出力を提供する。

### 2.2 非目的（v0.1ではやらない）

- マイグレーションの実行、ロールバック、gh-ost / pt-oscの起動
- スキーマの自動修正、SQLの自動書き換え（提案はするが、勝手に書き換えない）
- リアルタイム監視、ダッシュボード、Slack通知などの常駐機能
- Postgres / MariaDB / MySQL 5.7対応（v0.3以降で検討）
- テーブルデータの中身の検査（個人情報や業務データには触れない。カタログ情報のみ）
- 正確な分数の予測。I/O競合や同時負荷で変わるため、範囲と信頼度で表現するのが仕様である

---

## 3. ユーザーとユースケース

### 3.1 想定ユーザー

- アプリケーション開発者: PRにマイグレーションを含めるとき、自分で危険度を確認したい人
- レビュアー / DBA: 「このALTERは昼に流していいか」を根拠付きで判断したい人
- SRE / インフラ担当: デプロイ前のゲートとしてCIに組み込みたい人

### 3.2 ユースケース

1. ローカルでマイグレーションファイルをチェックする（開発時）
2. CIでマイグレーションのPRを自動チェックし、HIGH以上なら失敗させる（レビュー時）
3. 本番適用前の最終確認として、ブロッカーがないか確認する（適用直前）
4. 過去の実績を記録し、次回の見積もり精度を上げる（運用時）

---

## 4. 全体アーキテクチャ

```
マイグレーションファイル (.sql)
        │
        ▼
┌─────────────────┐
│  パーサー        │  Vitess sqlparser でAST化
│  (Go)           │  ALTER TABLE / CREATE INDEX 等を抽出
└────────┬────────┘
         ▼
┌─────────────────┐
│  分類エンジン    │  操作 × テーブル属性 × MySQLバージョン
│  (ルールベース)  │  → INSTANT / INPLACE / COPY / 不明 を判定
└────────┬────────┘
         ▼
┌─────────────────┐      ┌──────────────────────────┐
│  メタ情報収集    │◄────►│ MySQL（読み取り専用）      │
│  (catalog)      │      │ information_schema /     │
│                 │      │ performance_schema       │
└────────┬────────┘      └──────────────────────────┘
         ▼
┌─────────────────┐
│  見積もり・検査  │  時間範囲、ディスク、MDL、レプリカ、
│  (estimator)    │  外部キー、トリガー、PK有無などを評価
└────────┬────────┘
         ▼
┌─────────────────┐
│  レポート生成    │  人間向けMarkdown / テキスト
│  (report)       │  機械向けJSON
└─────────────────┘
         │
         ▼
  終了コード 0 / 1 / 2（第5.4章）
```

設計上の原則:

- 分類エンジンはDB接続なしで動く。SQLだけでも「操作の種類と理論上のアルゴリズム」は出る。
- メタ情報収集は失敗しても全体を止めない。取れなかった項目は「不明」としてレポートし、危険度を1段階保守的に上げる。
- 見積もりは必ず範囲で出す。較正データがない初期は幅を広く取り、信頼度を `low` と明記する。
- すべての判定にルールIDを付ける（例: `COPY-01`, `MDL-01`）。「なぜHIGHなのか」を追跡可能にする。

---

## 5. CLI仕様

### 5.1 コマンド一覧（v0.1）

| コマンド | 役割 |
|---|---|
| `mig-est check` | メイン機能。マイグレーションを診断する |
| `mig-est record` | 実際の適用時間を記録し、較正データに追加する |
| `mig-est version` | バージョンと対応MySQLバージョンを表示する |

`explain`、`diff`、`init` などはv0.2以降で検討し、v0.1では増やさない。

### 5.2 `check` の使い方

```bash
# 基本形: ファイル1つ＋DSN（環境変数）
export MIGEST_DSN="migread:xxxx@tcp(db.example.com:3306)/appdb"
mig-est check --file ./migrations/20261030_add_status.sql

# 複数ファイル、ディレクトリ指定
mig-est check --file ./migrations/20261030_*.sql
mig-est check --dir ./migrations --pattern "*.sql"

# DBに接続しないオフラインモード（サイズを手入力）
mig-est check --file ./migrations/20261030_add_status.sql \
  --offline --table-size orders=8200MB --table-rows orders=12400000

# 出力形式とCIゲート
mig-est check --file ./migrations/20261030_add_status.sql \
  --format json --fail-on high --output report.json

# 標準入力から
cat migration.sql | mig-est check --stdin
```

### 5.3 フラグ一覧

| フラグ | 必須 | デフォルト | 説明 |
|---|---|---|---|
| `--file` | いずれか必須 | - | マイグレーションファイル。複数指定可 |
| `--dir` | いずれか必須 | - | ディレクトリ内のSQLをファイル名順に診断 |
| `--stdin` | いずれか必須 | - | 標準入力からSQLを読む |
| `--dsn` | - | 環境変数 `MIGEST_DSN` | MySQL DSN。`--offline` 時は不要 |
| `--offline` | - | false | DBに接続しない。サイズ等はフラグで補う |
| `--table-size` | - | - | `table=サイズ` の繰り返し。例: `orders=8200MB` |
| `--table-rows` | - | - | `table=行数` の繰り返し |
| `--disk-free` | - | - | 利用可能ディスク容量。例: `50GB`。未指定時はディスク検査をスキップし「不明」と表示 |
| `--format` | - | `text` | `text` / `markdown` / `json` |
| `--output` | - | 標準出力 | レポートの出力先ファイル |
| `--fail-on` | - | `critical` | この危険度以上で終了コード1。`low` / `medium` / `high` / `critical` / `never` |
| `--config` | - | `./.mig-est.yaml`（あれば自動読込） | 設定ファイルパス |
| `--timeout` | - | `10s` | DBへの各クエリのタイムアウト |
| `--no-color` | - | false | カラー出力を無効化 |
| `--quiet` | - | false | サマリー1行のみ出力（CIログ向け） |

DSNの扱い:

- コマンドライン引数より環境変数を推奨する。シェル履歴にパスワードが残らないため。
- DSNはログ、レポート、エラーメッセージに絶対に出力しない。出力時は `user@tcp(host:port)/db` の形にマスクする。
- パスワードを含むDSNを `--help` の出力例には載せない。

### 5.4 終了コード

| コード | 意味 |
|---|---|
| 0 | 診断完了。危険度が `--fail-on` 未満 |
| 1 | 危険度が `--fail-on` 以上。CIを失敗させるためのコード |
| 2 | 使い方エラー、SQLパース失敗、DB接続失敗、設定ファイルエラーなど |

「診断したらHIGHだった」はエラーではなく、意図したゲート動作として終了コード1を返す。区別が重要。

### 5.5 設定ファイル `.mig-est.yaml`

```yaml
# すべて任意。未指定はデフォルト値を使う
mysql:
  version: "8.0"        # 8.0 / 8.4。未指定時は接続先から SELECT VERSION() で取得
thresholds:
  large_table_rows: 10000000      # この行数以上を「大テーブル」とみなす
  large_table_size_mb: 5000
  long_transaction_minutes: 5     # この時間以上のトランザクションをブロッカー扱い
  replica_lag_seconds: 10
  disk_safety_factor: 1.5         # COPY時に「テーブル＋インデックス × 係数」の空きを要求
fail_on: high
output:
  format: markdown
history:
  path: ./.mig-est/history.jsonl  # 較正データの保存先
  enabled: true
```

---

## 6. 入力仕様

### 6.1 SQLファイルの扱い

- 1ファイルに複数ステートメントを含んでよい。セミコロンで分割し、1ステートメントずつ診断する。
- コメント（`--`、`#`、`/* */`）はパーサーが処理する。コメント内のSQLは診断しない。
- `DELIMITER` 文、ストアドプロシージャ定義、トリガー定義はv0.1では対象外とし、「未対応」としてレポートする。誤って「安全」とは言わない。
- 文字コードはUTF-8を前提とする。

### 6.2 v0.1で診断対象とするステートメント

- `ALTER TABLE ...`（中心機能）
- `CREATE [UNIQUE | FULLTEXT | SPATIAL] INDEX ...`
- `DROP INDEX ...`
- `CREATE TABLE ...`（参考情報としてサイズ推定なしで分類のみ。主キーの有無などを指摘）
- `DROP TABLE ...`（参照元がある場合に警告）
- `RENAME TABLE ...`（メタデータ操作である旨を表示）

上記以外（`INSERT`、`UPDATE`、`CREATE VIEW`等）は「対象外」として一覧に出し、危険度には含めない。

### 6.3 複数操作を含むALTERの扱い

MySQLのALTERは複数操作を1文に書ける。分類は操作ごとに判定し、文全体の危険度は最も危険な操作のものとする。

例: `ADD COLUMN`（INSTANT）と `ADD PRIMARY KEY`（COPY）を同じ文に書いた場合、文全体はCOPY扱いになる。レポートでは「この文にはCOPYになる操作が含まれている」ことを明示し、分割を提案する。

### 6.4 ユーザー指定のALGORITHM / LOCKの検証

SQLに `ALGORITHM=...` や `LOCK=...` が明示されている場合、ツールは次の検証をする。

- 指定アルゴリズムで実行可能か。実行不可能な組み合わせなら、その場でエラーになることを事前に指摘する。
- 指定がない場合は、MySQLが暗黙に選ぶアルゴリズムを予測して表示する。
- 本番では `ALGORITHM=INSTANT, LOCK=NONE` のように期待値を明示し、「期待通りでなければ即エラーで止まる」書き方を推奨レポートに含める。暗黙のダウングレード（INSTANTのはずがCOPYになる）を防ぐための実践的な助言である。

---

## 7. 分類エンジン（MySQL 8.0 / 8.4）

### 7.1 判定の考え方

分類は「操作の種類」だけで決めない。次の4つを組み合わせる。

1. 操作の種類（ADD COLUMN、型変更、インデックス追加など）
2. テーブルの属性（行数、サイズ、ROW_FORMAT、FULLTEXTの有無、パーティション、トリガー、外部キー）
3. MySQLバージョン（特に8.0.29前後でINSTANTの範囲が変わる）
4. ユーザー指定のALGORITHM / LOCK

判定結果は必ず次のいずれかとし、根拠ルールIDを添える。

- `INSTANT`: メタデータのみ。行に触れない。
- `INPLACE`: その場で変更。行のコピーはしないが、インデックス構築や行フォーマット変更で時間がかかる場合がある。
- `COPY`: テーブル全体を新テーブルにコピーして置き換える。最も重い。
- `UNKNOWN`: パーサーが読めたが分類ルールにない、またはテーブル属性が不足して判定できない。UNKNOWNは「安全」ではなく「要手動確認」として扱い、危険度はMEDIUM以上にする。

### 7.2 分類マトリクス（v0.1）

MySQL 8.0のOnline DDLの考え方に沿う。INSTANTはメタデータのみで最も速く、INPLACEはその場で変更し、COPYはテーブル全体をコピーする、という3段階である。

| 操作 | 予測アルゴリズム（代表例） | 注意点 |
|---|---|---|
| ADD COLUMN（末尾、8.0.12以降） | INSTANT | 8.0.29以降は任意の位置でもINSTANT可。ただしROW_FORMAT=COMPRESSED、FULLTEXTあり、データディクショナリのテーブルスペース、一時テーブルは対象外 |
| ADD COLUMN（8.0.29未満で末尾以外） | INPLACE | 位置指定（AFTER / FIRST）でINSTANT対象外になるバージョン差に注意 |
| DROP COLUMN | INSTANT（8.0.29以降）/ INPLACE（それ以前） | 8.0.29以降は内部的に削除済みとしてマークされ、物理データは次回リビルドまで残る |
| ADD INDEX（セカンダリ） | INPLACE、LOCK=NONE可が多い | ソートと構築で時間はかかる。FULLTEXT / SPATIALは別扱い |
| DROP INDEX | INPLACE | 比較的軽いが、MDLは必要 |
| ADD PRIMARY KEY | COPY | 既存データの並び替えを伴うため重い |
| DROP PRIMARY KEY | COPY（多くの場合） | 同上 |
| MODIFY / CHANGE COLUMN（型変更） | 型の互換性で変わる | 互換性のある範囲（例：同系の型で長さ拡大など）はINPLACE、互換性のない型変更はCOPY。判定は型ペア表で管理する（7.3章） |
| SET / DROP DEFAULT | INSTANTが多い | デフォルト値の変更自体はメタデータ操作 |
| SET NOT NULL / DROP NOT NULL | INPLACEが多い | NOT NULL化は既存データの検証を伴う場合がある。NULLが混ざっていると実行時エラーになる点も指摘する |
| ADD FOREIGN KEY | INPLACE＋全行検証 | 既存データが参照整合性を満たすか検証スキャンが走る。データ量が多いと時間がかかる |
| DROP FOREIGN KEY | INPLACEが多い | 比較的軽い |
| ADD CHECK制約 | INPLACE＋全行検証 | 既存行の検証スキャンが発生する |
| RENAME COLUMN | INSTANT（8.0以降） | カラム名変更のみ |
| RENAME TABLE | メタデータ操作 | 短いMDLは必要だがリビルドはしない |
| CONVERT TO CHARACTER SET / COLLATE変更 | COPY | 文字コード変換は全行書き換えになる |
| ENGINE変更（例：MyISAM→InnoDB） | COPY | テーブル再構築になる |
| パーティション追加・削除 | 操作により異なる | v0.1ではパーティション操作はUNKNOWN寄りに保守的に扱い、手動確認を促す |
| ROW_FORMAT変更 | COPY | フォーマット変換は再構築になる |

この表は実装時の初期ルールであり、MySQLのバージョンアップや検証結果に応じて更新する。ルールはコード内にハードコードせず、テスト可能なテーブル（Goのマップ＋テストのゴールデンファイル）として持つ。

### 7.3 型変更の互換性判定

型変更は「型ペア表」で判定する。初期バージョンの考え方:

- 同じ型ファミリー内での拡大（例：`VARCHAR(50)` → `VARCHAR(100)`、`INT` → `BIGINT` など）は、原則INPLACE寄りとして扱うが、符号の有無（SIGNED / UNSIGNED）の変更や精度変更は別途検証する。
- 異なる型ファミリーへの変更（例：`VARCHAR` → `INT`、`DATE` → `VARCHAR`）はCOPY寄りとして扱う。
- `ENUM` / `SET` の要素追加・削除・順序変更は、値の再解釈を伴うため保守的に扱う。特に要素の削除や順序変更はデータの意味が変わる可能性があるため、HIGH以上の警告を出す。
- 判定に迷う型ペアはUNKNOWNに倒し、「手動でステージングで確認してください」と明記する。誤ってINSTANTと断定することだけは避ける。

型ペア表は `internal/classifier/type_compat.go`（仮）に置き、ユニットテストで全ペアを網羅する。

### 7.4 バージョン差の扱い

- 接続先の `SELECT VERSION()` からメジャー・マイナー（8.0.x / 8.4.x）を判定する。
- オフラインモードでは設定ファイルの `mysql.version` を使う。未指定時は8.0系の保守的な判定（8.0.29未満相当）にする。
- バージョンで結果が変わる操作（ADD COLUMNの位置、DROP COLUMNのINSTANT化など）は、レポートに「8.0.29以降ならINSTANT、未満ならINPLACE」のようにバージョン条件を明記する。

---

## 8. メタ情報収集（読み取り専用クエリ）

### 8.1 基本方針

- 実行するSQLは `SELECT` のみ。`SHOW` は情報取得のための読み取りとして許容するが、DDL / DMLは一切発行しない。
- 各クエリにタイムアウト（デフォルト10秒）を付け、失敗時はその項目を「不明」として続行する。
- 権限不足で読めない項目がある場合、どの項目が読めなかったかをレポートに明記し、読み取り専用ユーザーの作成SQL（第13章）を提示する。

### 8.2 収集項目と取得元

| 項目 | 取得元（代表） | 用途 |
|---|---|---|
| テーブル行数、データ量、インデックス量 | `information_schema.tables`（`TABLE_ROWS`、`DATA_LENGTH`、`INDEX_LENGTH`） | サイズ・時間見積もりの基礎。`TABLE_ROWS` はInnoDBでは推定値であることを注記する |
| カラム一覧、型、NULL可否、デフォルト | `information_schema.columns` | 型変更の互換性判定、ADD COLUMN位置の判定 |
| インデックス一覧 | `information_schema.statistics` | 既存インデックスの重複指摘、構築コスト推定 |
| 外部キー（参照する側・される側） | `information_schema.referential_constraints`、`key_column_usage` | 型変更や主キー変更時の影響範囲 |
| トリガー | `information_schema.triggers` | gh-ost利用可否、COPY時の注意喚起 |
| パーティション | `information_schema.partitions` | パーティション操作の保守的判定 |
| テーブルオプション | `SHOW CREATE TABLE`（読み取り） | ROW_FORMAT、CHARSET、ENGINEの確認 |
| 実行中トランザクション | `information_schema.innodb_trx`、`information_schema.processlist` | 長時間トランザクションの検出 |
| メタデータロック | `performance_schema.metadata_locks`、`performance_schema.threads` | MDLブロッカーの特定。performance_schemaが無効な場合は「取得不可」と明記 |
| レプリケーション状態 | `SHOW REPLICA STATUS`（8.0系）/ `SHOW SLAVE STATUS`（旧名称） | レプリカ遅延の検出。レプリカ構成でない場合はスキップ |
| サーバーバージョン、変数 | `SELECT VERSION()`、`SHOW VARIABLES LIKE 'innodb_online_alter_log_max_size'` 等 | バージョン差判定、オンラインALTERログ上限の確認 |

### 8.3 InnoDBの行数推定に関する注記

`information_schema.tables.TABLE_ROWS` はInnoDBでは正確な行数ではなく推定値である。レポートでは行数を「約」と表示し、見積もりも範囲で出すことで、この不確かさを吸収する。正確な行数が欲しい場合は `SELECT COUNT(*)` を流す選択肢もあるが、v0.1では大テーブルへのフルスキャンを避けるため実行しない。将来のオプションとして `--exact-count` を検討する。

---

## 9. 危険度とブロッカー検査

### 9.1 危険度レベル

| レベル | 意味 | CIでの扱い（デフォルト `--fail-on critical`） |
|---|---|---|
| LOW | INSTANT相当で、ブロッカーなし。昼でも流してよい可能性が高い | 通過 |
| MEDIUM | INPLACEで時間がかかる、または検証スキャンがある。時間帯や負荷に注意 | 警告として表示。`--fail-on medium` 指定時は失敗 |
| HIGH | COPY相当、または大テーブルへの重い操作。深夜帯やgh-ost等の検討が必要 | `--fail-on high` 指定時は失敗 |
| CRITICAL | ディスク不足、MDLブロッカーあり、主キーなしでgh-ostも使えない等、流してはいけない状態 | デフォルトで失敗 |

文全体の危険度は、含まれる操作の中で最も高いものとする。ブロッカー検査でCRITICALが出た場合、操作がINSTANTでも文全体はCRITICALになる。

### 9.2 ブロッカー検査一覧

| ID | 検査 | 判定（デフォルト閾値） | 対応 |
|---|---|---|---|
| MDL-01 | 長時間トランザクション | 5分以上継続するトランザクションが対象テーブルに触れている | 適用前にトランザクションの所有者を確認し、終了を待つか、適用を延期する |
| MDL-02 | 待機中のMDL | 対象テーブルでMDL待ちが発生している | 待機の原因を解消するまで適用しない |
| MDL-03 | アイドルなトランザクション | `Sleep` 状態でトランザクションを開いたままの接続がある | アプリケーション側のコミット漏れを疑う。安易なKILLはしない |
| REP-01 | レプリカ遅延 | 10秒以上の遅延 | 遅延が解消するまで待つ。オンラインDDL中は遅延が悪化しやすい |
| REP-02 | レプリカ停止 | レプリカSQL / IOスレッドが停止している | 停止原因を解消するまで適用しない |
| FK-01 | 参照されているテーブル | 対象テーブルを参照する外部キーがある | 型変更・主キー変更時は参照側への影響を確認する |
| FK-02 | 参照しているテーブル | 対象テーブルが他テーブルを参照している | 外部キー追加・削除時の検証範囲を確認する |
| TRG-01 | トリガーあり | 対象テーブルにトリガーがある | gh-ostはトリガー付きテーブルを扱えないため、代替手段を検討する |
| PK-01 | 主キーなし | 対象テーブルに主キーがない | gh-ost / pt-oscは主キー（またはユニークキー）を前提とするため、オンライン実行ツールの選択肢が狭まる |
| PART-01 | パーティションあり | パーティションテーブルである | パーティション操作は保守的に扱い、ステージングで確認する |
| DISK-01 | ディスク不足 | COPY時に必要な空き（テーブル＋インデックス × 1.5）が `--disk-free` を上回る、または未指定で判定不能 | ディスクを確保するか、gh-ost等の別手段を検討する。未指定時は「不明」としてMEDIUM以上にする |
| SIZE-01 | 大テーブルへの重い操作 | 1000万行以上または5GB以上のテーブルにCOPY / INPLACEリビルド | 深夜帯適用、またはgh-ost / pt-oscへの切り替えを検討する |
| FTS-01 | FULLTEXTあり | FULLTEXTインデックスがあるテーブルへのADD / DROP COLUMN | INSTANT対象外になる場合があるため、バージョンと操作を再確認する |
| ROWFMT-01 | ROW_FORMAT=COMPRESSED | COMPRESSEDテーブルへのINSTANT ADD COLUMN | INSTANT対象外のためINPLACE / COPYになる |
| LOG-01 | オンラインALTERログ上限 | 書き込み頻度が高いテーブルで、オンラインALTERログ（デフォルト128MiB）を超える懸念がある | 書き込み量が多い時間帯を避ける。v0.1では書き込みレートを直接測れないため、ヒューリスティックな警告に留める |

MDLに関する基本的な考え方として、ALTERは exclusive なメタデータロックを少なくとも一瞬必要とし、共有ロックを持つトランザクションが終わるまで待つ。待っている間、後続のクエリも詰まる可能性があるため、MDL検査は「ALTERの速さ」とは別軸の重大な検査である。

### 9.3 gh-ost / pt-osc が使えない条件の明示

COPY相当の操作で「gh-ostに逃げればいい」と安易に言わないため、次の条件をチェックしてレポートする。

- 主キーまたはユニークキーがない → gh-ostの前提を満たさない可能性が高い
- トリガーがある → gh-ostはトリガー付きテーブルを扱えない
- 外部キーで参照されている親テーブル → gh-ostの制約に当たる場合がある
- レプリカがない、または行ベースレプリケーションでない → gh-ostの前提を確認する

これらに当たる場合は「gh-ostでもそのままは使えません」と明記し、expand / contractパターン（新カラム追加→アプリ対応→旧カラム削除の段階的移行）への分割を提案する。

---

## 10. 見積もりモデル

### 10.1 基本方針

- 出力は必ず範囲（最小〜最大）と信頼度（`high` / `medium` / `low`）のセットで出す。
- 較正データがない初期状態は信頼度 `low` で、範囲を広く取る。嘘の精度を出さない。
- 見積もりは「ALTERそのものの処理時間」の目安であり、MDL待ち時間は含まない。MDL待ちはブロッカー検査で別途表示する。
- 単位は人間が読める形（秒、分、時間）で出し、JSONでは秒数も併記する。

### 10.2 計算式（v0.1）

```
COPY時間の目安:
  コピー対象サイズ = DATA_LENGTH + INDEX_LENGTH
  COPY時間 = コピー対象サイズ ÷ コピー速度
  ※ コピー速度の初期デフォルトは保守的に 50〜150 MB/s の範囲で扱い、
     較正データがたまったらそのDB固有の速度に置き換える

INPLACEインデックス構築の目安:
  対象行数 × 平均キー長 からインデックスサイズを概算し、
  ソート＋構築速度で割る。初期は範囲を広く取る

INSTANT:
  処理時間は1秒未満の想定。ただしMDL取得待ちは別途発生しうるため、
  「MDL待ちがなければ1秒未満」と明記する

検証スキャン（外部キー・CHECK追加時）:
  テーブルサイズ ÷ フルスキャン速度 で概算する
```

具体的な数値例（あくまで例示。実装時のデフォルトではない）:

- 8.2GB のテーブルを 100 MB/s でコピーすると約82秒。50 MB/s なら約164秒。
- このように「速度の仮定」をレポートに併記することで、読者が見積もりの根拠を検証できる。

### 10.3 ディスク見積もり

| 操作 | 必要空きの目安 |
|---|---|
| COPY | （DATA_LENGTH + INDEX_LENGTH）× 1.5。元テーブル＋新テーブル＋一時ファイル分 |
| INPLACEインデックス追加 | 新インデックスサイズ分＋ソート用一時領域 |
| INSTANT | ほぼ不要。ただしバイナリログやメタデータ分の余裕は別途考慮 |
| gh-ost利用時 | ゴーストテーブル分（元テーブルと同程度）＋バイナリログ増加分 |

`--disk-free` が未指定の場合、ディスク検査は「不明」としてスキップし、危険度を保守的に扱う。DBのSQLだけではホストのディスク空きを確実に取得できないため、ツール側で勝手に推測しないのが仕様である。

### 10.4 較正（calibration）

- `mig-est record` で、実際の適用時間と対象テーブル、操作、サイズを記録する。
- 保存形式はJSON Lines（`.mig-est/history.jsonl`）。1行1件で、git管理しやすい。
- 較正では、同一インスタンス（ホスト＋DB名で識別）かつ同一アルゴリズムの直近N件（デフォルト10件）の中央値を速度として使う。外れ値は中央値で吸収する。
- 較正データが3件未満の場合は信頼度を `low` のままにし、範囲を狭めない。
- 較正はあくまで同一環境での目安であり、他環境の速度をそのまま流用しない。

`record` の例:

```bash
# 実際に8分12秒かかった適用を記録する
mig-est record --file ./migrations/20261030_add_status.sql \
  --actual 8m12s --table orders

# JSON Linesの例（自動生成される）
# {"at":"2026-10-30T21:00:00+09:00","table":"orders","algorithm":"COPY",
#  "rows":12400000,"size_mb":8200,"actual_seconds":492,"host":"db.example.com"}
```

---

## 11. 安全な代替案の提案ルール

提案は「SQLを自動で書き換える」のではなく、「こう書き換えると安全になる」という具体例を示す。ユーザーが自分で判断して適用する。

| 元の操作 | 危険な点 | 提案 |
|---|---|---|
| `CREATE INDEX idx ON t (col)` | INPLACEでも大テーブルでは時間がかかり、書き込み負荷が上がる | `ALGORITHM=INPLACE, LOCK=NONE` を明示する。負荷が高い場合は深夜帯、またはgh-ost / pt-oscを検討する |
| `ALTER TABLE t MODIFY COLUMN c VARCHAR(255)` など型変更でCOPYになるもの | テーブル全体コピー＋長時間ロック | 互換性のある型変更にできないか確認する。できない場合は新カラム追加→バッチ移行→旧カラム削除のexpand / contractに分割する。またはgh-ost / pt-osc（制約を満たす場合） |
| `ALTER TABLE t ADD COLUMN c INT NOT NULL`（デフォルトなし） | 既存行への値補完や検証で重くなる場合がある | 3段階に分割する: ① NULL許可で追加 ② バッチで値を埋める ③ NOT NULL化（＋デフォルト設定） |
| `ALTER TABLE t ADD FOREIGN KEY ...` | 全行検証スキャン | 事前に `SELECT` で違反行がないか確認するSQL例を提示する。MySQLの外部キーにはPostgresの `NOT VALID` 相当がないため、検証コストを避けられない点を明記する |
| `ALTER TABLE t ADD PRIMARY KEY ...` | COPYで重い | 主キーが必要な理由を再確認する。オンライン実行ツールが使える条件（主キー以外のユニークキー等）を満たすか確認する |
| `ALTER TABLE t CONVERT TO CHARACTER SET utf8mb4` | 全行書き換え | 文字コード変換は不可逆的な側面もあるため、ステージングでの全量検証を強く推奨する。gh-ost / pt-oscでの実施も選択肢として提示する |
| 複数操作を1文にまとめたALTER | 最も重い操作に全体が引きずられる | 操作ごとに文を分割し、INSTANTなものとCOPYなものを分ける |
| ALGORITHM / LOCKの指定なし | 暗黙のダウングレードで想定外のCOPYになる | `ALGORITHM=INSTANT, LOCK=NONE` のように期待値を明示し、期待通りでなければ即エラーで止まる書き方を提案する |

提案SQLはレポート内にコードブロックで出し、コピーして使える形にする。ただし「この提案が必ず安全」ではなく「ステージングで確認してください」を併記する。

---

## 12. 出力仕様

### 12.1 人間向けテキスト出力（デフォルト）

```
$ mig-est check --file ./migrations/20261030_add_status.sql

対象テーブル: orders（約12,400,000行 / 8.2GB）
MySQL: 8.0.36 / InnoDB / ROW_FORMAT=DYNAMIC

[1] ALTER TABLE orders ADD COLUMN status VARCHAR(20) NOT NULL DEFAULT 'new'
    予測: INPLACE（ルール: ADD-COL-02）
    危険度: MEDIUM
    推定: 1〜4分（信頼度: low、較正データなし）
    注意: NOT NULL＋デフォルト付きの追加は、バージョンと条件でINSTANTにならない場合があります
    提案: NULL許可で追加 → バッチで埋める → NOT NULL化への分割を検討してください

[2] CREATE INDEX idx_orders_status ON orders (status)
    予測: INPLACE、LOCK=NONE可
    危険度: MEDIUM
    推定: 2〜6分（信頼度: low）
    提案: ALGORITHM=INPLACE, LOCK=NONE を明示してください

ブロッカー:
  ! MDL-01: 42分継続中のトランザクションが orders に触れています（connection_id: 12345）
  - REP-01: レプリカ遅延は2秒で、閾値内です

総合判定: HIGH（ブロッカーありのため、操作単体より1段階引き上げ）
終了コード: 1（--fail-on high のため）
```

表示ルール:

- 危険度は色付きで出す（HIGHは赤、MEDIUMは黄、LOWは緑）。`--no-color` で無効化。
- 長いSQLは全文ではなく、操作の要約＋元のSQLへの参照（ファイル名と行番号）を出す。
- 「不明」な項目は隠さず「不明」と表示する。

### 12.2 Markdown出力（`--format markdown`）

PRへの貼り付けや、GitHub Actionsでのコメント生成を想定する。見出し、表、コードブロックを使い、テキスト出力と同等の情報を含める。ファイル出力時は `--output report.md` と組み合わせる。

### 12.3 JSON出力（`--format json`）

機械処理とCI連携のための正規形式。スキーマは後方互換を意識し、フィールドの追加は許容、削除・型変更はメジャーバージョンアップでのみ行う。

```json
{
  "tool": "mig-est",
  "version": "0.1.0",
  "generated_at": "2026-10-30T21:00:00+09:00",
  "mysql": {
    "version": "8.0.36",
    "database": "appdb",
    "host_masked": "db.example.com:3306"
  },
  "overall_risk": "high",
  "statements": [
    {
      "index": 1,
      "file": "20261030_add_status.sql",
      "line": 1,
      "sql_summary": "ALTER TABLE orders ADD COLUMN status VARCHAR(20) NOT NULL DEFAULT 'new'",
      "table": "orders",
      "operation": "ADD_COLUMN",
      "predicted_algorithm": "INPLACE",
      "lock": "SHARED",
      "risk": "medium",
      "rule_ids": ["ADD-COL-02"],
      "estimate": {
        "min_seconds": 60,
        "max_seconds": 240,
        "confidence": "low",
        "basis": "table size 8200MB, assumed copy/sort speed range"
      },
      "disk_required_mb": null,
      "suggestions": [
        "NULL許可で追加 → バッチで埋める → NOT NULL化への分割を検討してください"
      ]
    }
  ],
  "blockers": [
    {
      "id": "MDL-01",
      "severity": "critical",
      "table": "orders",
      "message": "42分継続中のトランザクションが対象テーブルに触れています",
      "detail": {"connection_id": 12345, "trx_started": "2026-10-30T20:18:00+09:00"}
    }
  ],
  "unknowns": ["disk_free: --disk-free 未指定のためディスク検査をスキップしました"]
}
```

---

## 13. セキュリティと安全性

### 13.1 読み取り専用ユーザー

本番DBへの接続は、次のような読み取り専用ユーザーで行うことをREADMEに明記する。

```sql
-- mig-est用の読み取り専用ユーザーの例（権限は最小限にすること）
CREATE USER 'migread'@'%' IDENTIFIED BY '強固なパスワード';
GRANT SELECT ON appdb.* TO 'migread'@'%';
GRANT SELECT ON information_schema.* TO 'migread'@'%';
GRANT SELECT ON performance_schema.* TO 'migread'@'%';
-- レプリカ状態の確認が必要な場合は、環境に応じて次も検討する
-- GRANT REPLICATION CLIENT ON *.* TO 'migread'@'%';
FLUSH PRIVILEGES;
```

注意:

- `performance_schema` へのアクセスは環境によっては制限される。読めない場合はその項目を「不明」とする。
- 書き込み権限は一切不要であり、付与しないこと。
- Aurora / RDS / Cloud SQL などマネージド環境では、performance_schemaの有効化設定や権限が異なる場合がある。環境別の注記をREADMEに追記していく。

### 13.2 実行しないことの保証

- v0.1のコードベースに、DDL / DMLを発行するコードを入れない。`ALTER`、`CREATE`、`DROP`、`INSERT`、`UPDATE`、`DELETE` をDBへ送る経路を作らない。
- テストコードでも、本番相当のDBへ書き込むテストはDocker上のローカルDBに限定する。
- 将来、gh-ost起動などの実行機能を追加する場合は、別コマンド・別フラグ（例：`--execute`）として明示し、デフォルトでは絶対に実行しない設計にする。v0.1の段階でこの方針をREADMEに書いておく。

### 13.3 秘密情報の扱い

- DSN、パスワード、接続先の詳細はレポート・ログ・JSONに出力しない。ホストはマスク表示する。
- エラーメッセージにDSN全文を含めない。GoのドライバエラーにDSNが含まれる場合はマスク処理を入れる。
- 較正データ（history.jsonl）にはホスト名を含めるが、パスワードやユーザー名は含めない。historyファイルをgit管理する場合は、ホスト名が社内情報として問題ないか確認する旨をREADMEに書く。

---

## 14. 設定・履歴・CI連携

### 14.1 設定ファイルの探索順

1. `--config` で明示されたパス
2. カレントディレクトリの `.mig-est.yaml`
3. リポジトリルートの `.mig-est.yaml`（gitルートを遡って探索。v0.2で検討。v0.1ではカレントのみ）

設定ファイルがなくても動作する。すべてデフォルト値で動く。

### 14.2 履歴ファイル

- パス: 設定ファイルの `history.path`（デフォルト `./.mig-est/history.jsonl`）
- 形式: JSON Lines。1行が1件の適用実績。
- 記録する項目: 日時、テーブル、操作、予測アルゴリズム、行数、サイズ、実測時間、ホスト（マスク済みでもよい）、MySQLバージョン
- 記録しない項目: SQL全文（テーブル名やカラム名が社内情報になる場合があるため、デフォルトでは記録しない。`--include-sql` オプションでのみ記録可能とする）

### 14.3 GitHub Actionsでの利用例（v0.1で動作する形）

```yaml
name: migration-check
on:
  pull_request:
    paths:
      - 'migrations/**'

jobs:
  mig-est:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - name: Download mig-est
        run: |
          # リリースURLは公開時に確定する。以下は例
          curl -sSL -o mig-est https://github.com/<org>/mig-est/releases/download/v0.1.0/mig-est-linux-amd64
          chmod +x mig-est
      - name: Check migrations
        env:
          MIGEST_DSN: ${{ secrets.MIGEST_DSN }}  # 読み取り専用ユーザーで、ステージングDBを指すこと
        run: |
          ./mig-est check --dir ./migrations \
            --format markdown --output mig-est-report.md \
            --fail-on high
      - name: Upload report
        if: always()
        uses: actions/upload-artifact@v4
        with:
          name: mig-est-report
          path: mig-est-report.md
```

CIでの注意点:

- CIから本番DBに直接接続しない。ステージングDB、または本番と同等サイズのシャドーDBを指すのが原則。
- ステージングのサイズが本番より小さい場合、見積もりは過小になる。レポートに「この診断はステージングDBのサイズに基づいています」と明記されるよう、接続先のDB名とサイズをレポートに含める。
- PRコメントへの自動投稿はv0.2で検討する。v0.1ではレポートのアーティファクト保存に留める。

---

## 15. プロジェクト構成（Go）

```
mig-est/
├── README.md
├── LICENSE
├── .mig-est.yaml.example
├── go.mod
├── cmd/
│   └── mig-est/
│       └── main.go          # CLIエントリポイント。フラグ定義とコマンド振り分けのみ
├── internal/
│   ├── cli/                 # フラグ、設定ファイル読み込み、終了コード
│   ├── parser/              # Vitess sqlparserのラッパー。AST→操作抽出
│   ├── classifier/          # 分類マトリクス、型互換表、バージョン差ルール
│   │   ├── matrix.go
│   │   ├── type_compat.go
│   │   └── rules_test.go    # ゴールデンテスト
│   ├── catalog/             # MySQLへの読み取り専用クエリ群
│   │   ├── tables.go
│   │   ├── locks.go
│   │   └── replication.go
│   ├── estimator/           # 時間・ディスク見積もり、較正
│   ├── checker/             # ブロッカー検査（MDL、レプリカ、FK、トリガー等）
│   ├── suggester/           # 安全な代替案の提案ルール
│   ├── report/              # text / markdown / json レンダラー
│   └── history/             # history.jsonlの読み書き、較正計算
├── testdata/
│   ├── migrations/          # 分類テスト用のSQLゴールデンファイル
│   └── expected/            # 期待する分類結果（JSON）
├── docker/
│   └── docker-compose.yml   # 統合テスト用MySQL 8.0 / 8.4
└── docs/
    └── spec.md              # この仕様書（リポジトリ内では docs/spec.md に置く）
```

依存ライブラリ（v0.1の想定。採用時にライセンスとサイズを確認すること）:

| 用途 | 候補 | 備考 |
|---|---|---|
| MySQLドライバ | `github.com/go-sql-driver/mysql` | GoのMySQLドライバとして最も広く使われている。DSN形式に注意 |
| SQLパーサー | `vitess.io/vitess/go/vt/sqlparser` | MySQL方言のパーサーとして実績がある。Vitess全体への依存が重い場合は、sqlparserパッケージだけの利用可否をスパイクで検証する |
| CLIフレームワーク | `github.com/spf13/cobra`（任意） | フラグが増えたら導入を検討。v0.1初期は標準 `flag` パッケージでもよい。無理に導入しない |
| 設定ファイル | `gopkg.in/yaml.v3` | `.mig-est.yaml` 読み込み用 |
| テスト用DB | Docker上のMySQL 8.0 / 8.4 | testcontainersの利用も検討するが、まずはdocker-composeで十分 |

依存を増やす判断基準: 「その依存なしで書くと、バグの温床になるか」。パーサーとドライバは自作しない。それ以外は標準ライブラリで済ませる。

---

## 16. 言語選定：なぜGoか

### 16.1 結論

**Goを推奨する。** プロトタイプから公開版までGo一本で進めるのが最も無駄が少ない。

### 16.2 比較表

| 言語 | メリット | デメリット | mig-estとの相性 |
|---|---|---|---|
| Go（推奨） | 単体バイナリ配布が簡単。クロスコンパイルが標準。MySQLドライバが成熟。VitessのMySQLパーサーがGo製でそのまま使える。gh-ostもGo製で、MySQLオンラインDDLの知見がGoエコシステムに集まっている | パーサー依存（Vitess）が重い可能性がある。ジェネリクスや型システムは控えめ | ◎。配布・パーサー・ドライバの3点が揃う唯一の選択肢 |
| Rust | 単体バイナリ、性能、型安全性が高い | MySQLパーサー・ドライバのエコシステムがGoほど成熟していない。開発速度が遅くなりやすい。学習コストが高い | ○。性能は不要なツールなので、Rustの強みを活かしにくい |
| Python | プロトタイプが最も速い。sqlglotなどSQL解析ライブラリが優秀。書き捨ての検証には最適 | 単体バイナリ配布が弱い（PyInstaller等で手間とサイズ増）。CIで使う側にPython環境を要求しやすい。実行速度は問題にならないが、配布の弱さがOSSとしての採用障壁になる | △。検証用スパイクには最適だが、公開版の本命にはしにくい |
| TypeScript / Bun | 書き慣れている人には速い。Bunなら単体バイナリ化も可能 | MySQLパーサーやオンラインDDLの知見がGoほど集まっていない。CLIツールとしての配布実績がGoより少ない | ○。TypeScriptに強い人なら選択肢になるが、パーサーで回り道する可能性が高い |
| Ruby / PHP | 既存のマイグレーションツールと親和性がある場合がある | 単体バイナリ配布が弱い。CLIツールとしての新規OSSでは採用されにくい | △ |

### 16.3 Goを選ぶ具体的な理由

1. **パーサーがGo製であることが決定的。** VitessのsqlparserはMySQL方言をAST化する実績あるライブラリで、Goからそのまま使える。Pythonのsqlglotも優秀だが、Goを選べばパーサー・ドライバ・バイナリ配布が同一言語で揃う。
2. **配布が楽。** `go build` で静的バイナリができ、GitHub Releasesに置くだけでユーザーはダウンロードしてすぐ使える。CIでの利用ハードルが最も低い。
3. **gh-ostと同じ言語。** MySQLのオンラインDDL周辺の実装例、ブログ、コードリーディングの資産がGoに集まっている。困ったときに読むべきコードが同じ言語であることは、個人開発では大きい。
4. **性能は十分で、並行処理も素直。** 複数テーブル・複数ファイルの診断を並行化したい場合も、goroutineで素直に書ける。ただしv0.1では並行化は必須ではない。
5. **学習コストが回収できる規模。** このツールはCLI＋SQLパース＋DB読み取りという、Goの得意分野に収まる。Go未経験でも、1〜2週間で書ける範囲に収まる見込みがある。

### 16.4 Go未経験の場合の進め方

- いきなりVitessのフル活用を目指さない。まずは `parser` パッケージで「ALTER TABLEのテーブル名と操作種別が取れる」最小のスパイクを1〜2日で作り、Vitess依存の重さとパース成功率を確認する。
- スパイクで「Vitessが重すぎる」「パースできない方言が多い」と判明したら、その時点で代替案（TiDBパーサー等のGo製パーサー、またはsqlparserの軽量フォーク）を検討する。言語自体を変える判断は、スパイクの結果が出てからで遅くない。
- Pythonで1日スパイクして分類ルールの正しさを検証し、その後Goに移植する、という2段階もあり得る。ただし移植コストを考えると、最初からGoでスパイクする方が総工数は少ない。

### 16.5 採用するGoのバージョンとツール

- Goはリリース時点で最新の安定版の1つ前以降を推奨（例：公開時に1.24系が安定版なら1.24以降）。仕様書では特定バージョンに固定せず、READMEに記載する。
- 配布はGoReleaserを検討する。GitHub Releasesへのバイナリ配置、チェックサム生成を自動化できる。
- フォーマットは `gofmt`、静的解析は `go vet` を最低限CIに入れる。`golangci-lint` はv0.2以降で検討。

---

## 17. テスト計画とv0.1の完成条件

### 17.1 テストの種類

| 種類 | 内容 |
|---|---|
| ユニットテスト（分類） | SQL→分類のゴールデンテスト。`testdata/migrations/` のSQLと `testdata/expected/` の期待結果を突き合わせる。型ペア表も全ペアをテストする |
| ユニットテスト（見積もり） | サイズと速度の仮定から、範囲計算が正しいことをテストする。実測値の正確さではなく、計算式の正しさをテストする |
| 統合テスト（Docker） | docker-composeでMySQL 8.0 / 8.4を立て、実際にテーブルを作り、メタ情報収集とブロッカー検査が動くことを確認する |
| 統合テスト（MDLブロッカー） | 別セッションで長時間トランザクションを開いた状態で `check` を実行し、MDL-01が検出されることを確認する |
| ベンチマークテスト | 100万行、1000万行のテーブルで実際にALTERを流し、推定範囲と実測値を比較して記録する。READMEに結果を載せる |

### 17.2 v0.1の完成条件（Definition of Done）

すべて満たしたらv0.1として公開する。

- [ ] `mig-est check` が、MySQL 8.0の代表的なALTER 20パターン以上を正しく分類できる（ゴールデンテスト通過）
- [ ] 読み取り専用接続で、テーブルサイズ・行数・外部キー・トリガー・MDLブロッカーを取得できる
- [ ] COPY / INPLACE / INSTANTの3段階で危険度を出し、HIGH以上でCIを失敗させられる
- [ ] 見積もりが範囲＋信頼度で出て、較正データがない場合は `low` と明記される
- [ ] 安全な代替案が、少なくとも主要な危険操作5種類以上で提示される
- [ ] JSON出力とMarkdown出力が動作する
- [ ] Docker上のMySQLで、100万行・1000万行のベンチ結果をREADMEに載せている
- [ ] READMEに「実行しないこと」「読み取り専用ユーザーの作り方」「ステージングDBを使うこと」が明記されている
- [ ] GitHub ReleasesでLinux / macOSバイナリがダウンロードできる

### 17.3 開発スケジュールの目安（個人開発・副業ペース想定）

| 週 | 内容 |
|---|---|
| 1週目 | スパイク：VitessパーサーでALTERが読めるか、go-sql-driverで読み取り専用接続ができるかを確認する。分類マトリクスの初期版を書く |
| 2週目 | 分類エンジン＋CLIの骨格＋テキスト出力。ゴールデンテストを整備する |
| 3週目 | メタ情報収集（カタログ、MDL、レプリカ）とブロッカー検査 |
| 4週目 | 見積もり＋代替案提案＋JSON / Markdown出力 |
| 5週目 | Docker統合テスト＋ベンチマーク＋README整備 |
| 6週目 | バッファ。GitHub Releases、CI連携例、公開準備 |

これは目安であり、週あたりの確保時間が変われば伸び縮みする。削ってはいけないのは「分類の正確さ」と「実行しないことの保証」の2点である。

---

## 18. ロードマップ

### v0.1（プロトタイプ〜初公開）

- MySQL 8.0 / 8.4、InnoDB、CLI、分類、ブロッカー検査、見積もり（範囲＋較正）、JSON / Markdown出力、CIゲート

### v0.2

- GitHub ActionsでのPRコメント投稿
- 設定ファイルのgitルート探索
- Homebrew配布
- MariaDBの差分調査（対応するかどうかは需要を見て判断）

### v0.3

- Postgres対応の検討（分類エンジンの抽象化が必要になるため、v0.1の段階でDB依存部分は `catalog/mysql` のように分離しておく）
- `--exact-count` オプションの検討
- 書き込みレートの推定（バイナリログやperformance_schemaからの取得可否を調査）

### v0.4以降（需要次第）

- gh-ost / pt-oscコマンドの自動生成（実行はしない。コマンド例の提示に留める）
- 複数DB・複数環境の一括診断
- 過去の診断結果の比較（前回より危険度が上がった場合に警告）

---

## 19. リスクと未決定事項

| リスク / 未決定事項 | 影響 | 対応方針 |
|---|---|---|
| Vitess依存が重い、またはパースできない方言が多い | 分類の正確さとバイナリサイズに影響 | 1週目のスパイクで検証する。ダメならTiDBパーサー等の代替を検討する。言語変更は最後の手段 |
| MySQLのバージョン差（8.0.29前後、8.4の変更）で分類がずれる | 誤判定の原因になる | バージョン条件をルールに明記し、テストは8.0と8.4の両方で回す |
| `TABLE_ROWS` が推定値である | 見積もりの幅が広がる | 範囲表示と信頼度で吸収する。`--exact-count` は将来オプションとして検討 |
| ホストのディスク空きがSQLだけでは取得できない | ディスク検査が「不明」になる | `--disk-free` による手入力を仕様とし、勝手に推測しない |
| performance_schemaが無効な環境がある | MDL検査ができない | 「取得不可」と明記し、代替として `SHOW PROCESSLIST` ベースの簡易検査を残す |
| Aurora / RDS / Cloud SQLでの権限・変数の違い | 一部検査が動かない | 環境別の注記をREADMEに蓄積する。動かない項目は隠さず表示する |
| 見積もりの信頼性への過度な期待 | 「当たらなかった」と評価される | 最初から「範囲＋信頼度」「較正で育つ」ことをREADMEとレポートに明記する |
| プロジェクト名 `mig-est` の重複 | 既存ツールと名前が被る可能性 | 公開前にGitHub / 検索で重複確認し、被る場合は改名する。仕様書内では仮称として扱う |

---

## 20. 参考資料（実装時に確認すること）

- MySQL 8.0 Reference Manual: InnoDB Online DDL Operations（オンラインDDLの操作別対応表）
- MySQL 8.0 Reference Manual: Online DDLのアルゴリズムとロックの説明（INSTANT / INPLACE / COPY、LOCK=NONE / SHARED / EXCLUSIVE）
- MySQL 8.0 Reference Manual: performance_schemaのmetadata_locksテーブル
- Vitessドキュメント: schemadiff / sqlparserの使い方
- gh-ost / pt-online-schema-changeのドキュメント: オンライン実行ツールの制約（主キー、トリガー、外部キーなど）
- MySQL 8.0.29のリリースノート: INSTANT ADD / DROP COLUMNの拡張に関する記述

---

## 付録A. サンプルレポート（Markdown出力の全体例）

```markdown
# mig-est レポート

- 対象DB: appdb（MySQL 8.0.36、ステージングDBのサイズに基づいています）
- 総合判定: HIGH
- 診断日時: 2026-10-30T21:00:00+09:00

## ステートメント

### [1] ALTER TABLE orders ADD COLUMN status VARCHAR(20) NOT NULL DEFAULT 'new'

- 予測: INPLACE（ルール: ADD-COL-02）
- 危険度: MEDIUM
- 推定: 1〜4分（信頼度: low）
- 提案: NULL許可で追加 → バッチで埋める → NOT NULL化への分割を検討してください

### [2] CREATE INDEX idx_orders_status ON orders (status)

- 予測: INPLACE、LOCK=NONE可
- 危険度: MEDIUM
- 推定: 2〜6分（信頼度: low）
- 提案: ALGORITHM=INPLACE, LOCK=NONE を明示してください

## ブロッカー

- [CRITICAL] MDL-01: 42分継続中のトランザクションが orders に触れています
- [INFO] REP-01: レプリカ遅延は2秒で、閾値内です

## 不明な項目

- disk_free: --disk-free 未指定のためディスク検査をスキップしました
```

## 付録B. 読み取り専用ユーザーの作成SQL（再掲）

```sql
CREATE USER 'migread'@'%' IDENTIFIED BY '強固なパスワード';
GRANT SELECT ON appdb.* TO 'migread'@'%';
GRANT SELECT ON information_schema.* TO 'migread'@'%';
GRANT SELECT ON performance_schema.* TO 'migread'@'%';
FLUSH PRIVILEGES;
```

## 付録C. 悪いマイグレーションと良いマイグレーションの例

```sql
-- 悪い例: 暗黙のアルゴリズムに任せている。大テーブルではCOPYになる可能性がある
ALTER TABLE orders MODIFY COLUMN status VARCHAR(30);

-- 良い例: 期待するアルゴリズムとロックを明示し、期待通りでなければ即エラーで止まる
ALTER TABLE orders MODIFY COLUMN status VARCHAR(30),
  ALGORITHM=INPLACE, LOCK=NONE;

-- 悪い例: 複数操作を1文にまとめ、重い操作に引きずられる
ALTER TABLE orders ADD COLUMN note TEXT, ADD PRIMARY KEY (id);

-- 良い例: INSTANTな操作と重い操作を分ける
ALTER TABLE orders ADD COLUMN note TEXT, ALGORITHM=INSTANT;
-- 別文・別タイミングで主キー変更を検討する
```
