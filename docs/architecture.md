# mig-est ディレクトリ構成と役割解説

`mig-est` は、MySQL（8.0 / 8.4 InnoDB）における DDL（`ALTER TABLE` 等）の実行前に、ロック、所要時間、アルゴリズム（`INSTANT` / `INPLACE` / `COPY` / `UNKNOWN`）や危険性を事前診断する読み取り専用 CLI ツールです。

---

## 全体アーキテクチャと処理フロー

マイグレーション診断時のデータの流れは以下のパイプラインになっています。

```text
[SQLファイル] ───> parser ───> classifier ───> checker ───> estimator ───> suggester ───> report
                      │              │            │             │
[DB / 引数] ──────────┴──────────────┴────> catalog             │
                                                                │
[過去の実測ログ] ──────────────────────────────────────────> history
```

---

## 主要ディレクトリの解説

### 1. `cmd/`
Go の標準的なプロジェクトレイアウトに従った、**CLI バイナリのエントリポイント**です。

- **`cmd/mig-est/main.go`**
  - アプリケーション起動時の `main()` 関数のみを持つ薄いラッパーです。
  - コマンドライン引数を `internal/cli.Run()` に渡し、返された終了コードで `os.Exit()` を呼び出します。
  - コードのテスタビリティを確保するため、具体的なビジネスロジックはすべて `internal/` 配下に委譲されています。

---

### 2. `internal/`
Go の `internal` パッケージ規約により外部リポジトリからはインポートできない、**`mig-est` のコアロジック群**が集約されています。単一責任の原則に基づき、パッケージごとに責務が明確に分離されています。

| パッケージ | 役割・処理内容 |
| :--- | :--- |
| **`internal/cli`** | **CLI コマンドのディスパッチと全体制御**<br>・サブコマンド（`check`, `record`, `version`）の振り分け。<br>・コマンドライン引数（位置引数ファイル指定、`--file`, `--offline`, `--table-size`, `--fail-on` 等）の解析とバリデーション。<br>・各内部コンポーネント（parser, catalog, classifier, checker 等）を順に呼び出すパイプラインのオーケストレーション。 |
| **`internal/parser`** | **SQL の構文解析（AST パース）**<br>・Vitess の SQL パーサー（`vitess.io/vitess/go/vt/sqlparser`）を採用。<br>・DDL 文（`ALTER TABLE`, `CREATE/DROP INDEX`, `CREATE/DROP TABLE`, `RENAME TABLE`）から対象テーブル、追加・変更カラム、インデックス、制約定義を抽出。<br>・サポート外の構文（トリガー、ストアドプロシージャ、`DELIMITER` ブロック、DML 等）を早期検出。 |
| **`internal/classifier`** | **実行アルゴリズムの予測・分類**<br>・MySQL のバージョンやテーブル構造に基づき、MySQL が選択するアルゴリズム（`INSTANT` / `INPLACE` / `COPY` / `UNKNOWN`）やロック種別を判定。<br>・MySQL 8.0.29 前後の `INSTANT ADD/DROP COLUMN` の仕様差分や型互換マトリクス（整数・文字列・DECIMAL・ENUM/SET 等）を考慮。<br>・複合操作（例: カラム追加と主キー追加が同居）の場合に最も重い操作（`COPY`）へ引き上げられる仕様をモデル化。 |
| **`internal/catalog`** | **MySQL からのメタデータ収集（オンラインモード用）**<br>・安全のため `SELECT` と `SHOW` の読み取り専用クエリのみを発行（DDL/DML は一切発行しない）。<br>・`information_schema` 等からテーブルサイズ、行数、`ROW_FORMAT`、インデックス、外部キー、トリガー、パーティションを収集。<br>・`performance_schema` 等から MDL（メタデータロック）待ち、長時間トランザクション、アイドルトランザクション、レプリカラグ等の稼働状況スナップショットを取得。 |
| **`internal/checker`** | **リスク・ブロッカーの検査**<br>・収集したメタデータやオンラインスナップショットから危険因子を診断。<br>・「主キー欠如」「外部キー・トリガーの存在」「ディスク空き容量不足（テーブルサイズの1.5倍未満など）」「長時間トランザクション・MDL待機」などの警告・エラー（Finding）を生成。 |
| **`internal/estimator`** | **所要時間と必要ディスク容量の試算**<br>・テーブル容量（MB）とアルゴリズム（`COPY` / `INPLACE`）のスループット仮定から、所要時間を幅（例: `55s to 2m 44s`）と信頼度（confidence）として算出（断定的な単一数値は出さない方針）。<br>・`history` の実績記録が3件以上あれば中央値を用いてスループットを自動キャリブレーション。 |
| **`internal/suggester`** | **安全な代替案の提示**<br>・`COPY` になる操作に対して「Expand/Contract パターンの利用」「前提条件を満たす場合の gh-ost / pt-online-schema-change の検討」を提案。<br>・暗黙的なフォールバックを防ぐための `ALGORITHM=INSTANT, LOCK=NONE` 明示の提案、外部キー追加前の孤立行チェック SQL、複数操作 `ALTER` の分割提案などを生成。 |
| **`internal/report`** | **診断結果のフォーマット出力**<br>・ターミナル表示用のテキスト（`text`）、PR コメント・アーティファクト用の Markdown（`markdown`）、CI 連携・自動判定用の JSON（`json`）を生成。 |
| **`internal/history`** | **実行実績ログの永続化（キャリブレーション用）**<br>・`mig-est record` で入力された過去のマイグレーション実績（テーブル名、サイズ、実測時間等）を `.mig-est/history.jsonl` に保存・読み込み（セキュリティのため SQL 本文や接続情報は記録しない）。 |
| **`internal/model`** | **共通ドメインモデルの定義**<br>・`Statement`, `Finding`, `Risk`, `Algorithm`, `Severity`, `Report` など、パッケージ間を跨いで利用される共通の型・構造体を定義。 |
| **`internal/config`** | **設定ファイルのロード**<br>・`.mig-est.yaml` を読み込み、閾値（ラージテーブル判定行数・容量、長時間トランザクション分数、レプリカラグ許容秒数、CI ゲート `--fail-on` レベルなど）を設定。 |
| **`internal/mysqlver`** | **MySQL バージョン解析**<br>・MySQL 8.0.x / 8.4.x のバージョン文字列をパースし、特定バージョン以降で利用可能な機能フラグを判定。 |
| **`internal/version`** | **ビルドバージョン管理**<br>・`Makefile` や GitHub Actions から `-ldflags` で埋め込まれるバージョン・Git コミット・ビルド日時を保持。 |

---

### 3. `testdata/`
ユニットテストやゴールデンテスト（回帰テスト）で使用される **テストフィクスチャ（検証データ）** が格納されています。

- **`testdata/migrations/`**
  - 代表的な MySQL 8.0 DDL パターン 28 件（`ALTER TABLE` / インデックス操作 26 パターン、`CREATE TABLE`、未サポートのトリガー定義）の SQL ファイル群です。
  - `INSTANT` カラム追加・リネーム・デフォルト変更・不可視インデックス変更、`INPLACE` インデックス追加/削除・外部キー・CHECK 制約・8.0.29 未満の位置指定カラム追加/削除、`COPY` 文字コード変換・主キー追加/削除・エンジン/行フォーマット変更・`FORCE`、不正な `ALGORITHM=INSTANT` 指定（`CRITICAL` 検出）、パーティション操作（`UNKNOWN`）などを網羅しています。
- **`testdata/expected/classification.json`**
  - `migrations/` 配下の各 SQL に対し、分類器（`classifier`）が出力すべき期待結果（予測アルゴリズム、リスクレベル、適用ルール ID）を定義したゴールデンファイルです。
  - `internal/classifier/fixtures_test.go` で全ファイルの網羅性と判定結果が自動検証され、分類ルールのリグレッションを検知します。
- **`testdata/README.md`**
  - テストフィクスチャの利用方針と更新ルールについてのドキュメントです。

---

## その他のディレクトリ

- **`docs/`**
  - **`docs/spec.md`**: ツールの要件定義、アルゴリズムマトリクス、リスク判定ルール、ベンチマーク記録をまとめた仕様書（英語）です。
  - **`docs/architecture.md`**: 本ドキュメント（ディレクトリ・パッケージ構成解説）です。
- **`docker/`**
  - 動作確認・統合テスト用の MySQL 8.0 および MySQL 8.4 を立ち上げる `docker-compose.yml` と、読み取り専用アカウント（`migread`）を含む初期スキーマ投入スクリプト（`init/01-schema.sql`）が含まれます。
- **`bin/` / `dist/`**
  - `make build`（ローカル向け `bin/mig-est`）および `make dist`（Linux/macOS マルチアーキテクチャ向け配布バイナリと `SHA256SUMS`）の出力先です（Git 管理対象外）。
- **`.github/workflows/`**
  - **`ci.yml`**: PR・`main` ブランチ push 時のフォーマット検査・静的解析（`go vet`）・レース検出テスト（`go test -race`）・オフラインスモークテストを実行します。
  - **`release.yml`**: タグ push または手動実行（自動パッチ採番対応）により、Linux/macOS（amd64/arm64）の静的バイナリと `SHA256SUMS` をビルド・検証し、リリースノートを自動生成して GitHub Releases へ公開します。
