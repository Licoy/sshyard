<p align="left"><a href="README.md">English</a> · <a href="README_ZH.md">简体中文</a> · <a href="README_ZH_TW.md">繁體中文</a> · <a href="README_KO.md">한국어</a> · <a href="README_JA.md"><strong>日本語</strong></a></p>

<p align="center">
  <img src="resources/icon.png" alt="SSHYardのロゴ" width="128" height="128">
</p>

<h1 align="center">SSHYard</h1>

SSHYardは、macOS、Windows、Linux向けのネイティブデスクトップSSH / SFTPクライアントです。ターミナル、ファイル管理、AIアシスタントを1つのアプリにまとめています。

## スクリーンショット

<table>
  <tr>
    <td width="50%" align="center">
      <img src="docs/terminal-ai-assistant.webp" alt="ターミナルとAIアシスタント" width="100%"><br>
      ターミナルとAIアシスタント
    </td>
    <td width="50%" align="center">
      <img src="docs/sftp-file-manager.webp" alt="SFTPファイルマネージャー" width="100%"><br>
      SFTPファイルマネージャー
    </td>
  </tr>
  <tr>
    <td width="50%" align="center">
      <img src="docs/system-monitor.webp" alt="システムモニター" width="100%"><br>
      システムモニター
    </td>
    <td width="50%" align="center">
      <img src="docs/ai-assistant-settings.webp" alt="AIアシスタントの設定" width="100%"><br>
      AIアシスタントの設定
    </td>
  </tr>
</table>

## 機能

- **ターミナル**
  - タブ、ペイン分割、ターミナル内検索。タブは並べ替えと名前の変更が可能
  - トゥルーカラー、全角文字、入力メソッドに対応し、tmux、vim、htopでマウス操作が可能
  - 10種類の内蔵カラースキームと`.itermcolors`のインポート、キーワードのハイライト、行番号とタイムスタンプ、カーソル・行の高さ・文字幅の調整
  - 多数の出力エンコーディングをホストごとに設定可能、⌘ / Ctrl + クリックでターミナル内のURLを開く、⌥ / Alt + ドラッグで矩形選択
  - 入力中にホストのシェル履歴からコマンドを提案し、→ または End で確定、フォルダ内のファイル一覧からパスを補完
  - SSHに加えてTelnet、シリアルポート、ローカルターミナルに対応（WindowsではPowerShell、CMD、Git Bash、WSLを選択可能）
  - `sshyard --path <フォルダー>`、または Finder でフォルダーの「このアプリケーションで開く → SSHYard」を選んで、指定したフォルダーにローカルターミナルを開く。アプリ実行中なら既存のウィンドウで開きます
  - 同じホストのペイン間で接続を共有し、自動再接続にも対応。ネットワークの切り替えやスリープ解除の直後に再接続し、元のフォルダーに戻ります

- **ワークスペース**
  - システムモニター、プロセスの使用状況、ホストの詳細、Docker、tmux、スニペット、コマンド履歴、コマンドの一斉送信、セッションログはドラッグできるペインで、任意のペインの横や別のタブに配置可能
  - 再接続後に元のセッションへ戻るtmuxセッション管理
  - Dockerコンテナーとイメージの詳細とリソース使用量を表示し、コンテナーの起動・停止も可能
  - 入力の同期に対応したコマンドの一斉送信と、テキストまたは生データ形式のセッションログ
  - コマンドパレットとカスタマイズ可能なショートカット、ウィンドウと分割レイアウトの記憶

- **ファイル管理**
  - ターミナルと並べて使えるSFTPとディレクトリ追従。サーバーにSFTPがなければ自動的にSCPを使用
  - ディレクトリツリー付きのタブ式内蔵エディター
  - 必要に応じてsudoで権限を昇格してファイルにアクセス
  - ドラッグ＆ドロップとフォルダー転送に対応し、アップロード時に権限と更新日時を保持
  - ドラッグ＆ドロップやコピー＆ペーストで、サーバー間でファイルやフォルダーを直接コピー。データはアプリのメモリを経由し、ローカルディスクには書き込まない
  - 一時停止、再開、再試行ができ、転送中はコンピューターをスリープさせない転送キュー

- **AIアシスタント**
  - ホストごとの専用アシスタントからコマンド実行やファイル編集が可能。SSH、Telnet、シリアル、ローカルターミナルのいずれでも使え、会話はホストごとに保存
  - AIプロバイダーへの接続、またはこのコンピューターで自動検出したACPエージェント（Claude Code、Codex、Grok、Gemini CLIなど）の利用。複数を同時に追加でき、それぞれが対応するモデルと思考レベルを選べます。作業は常にリモートホスト上で行われます
  - 画像やファイルを貼り付けて添付
  - 「確認」「審査」「フルアクセス」の各モードと、審査モデル・1ターンのツール呼び出し上限・リクエストのタイムアウトの設定

- **エージェント連携**
  - 公開API、MCP、スキル経由でローカルAIエージェントを接続
  - 保存済み認証情報を公開せずに、ホスト一覧の取得、接続、コマンド実行が可能
  - Claude CodeとCodex向けのスキルをワンクリックで設定、エージェントアクセスは初期状態で無効

- **ホストと認証情報**
  - SSH設定からのインポート、Xshell、MobaXterm、PuTTYなど10種類のクライアントからの移行、テキストでの一括インポート、または`user@host:port`でのクイック接続
  - パスワード、OpenSSH / PEM / PuTTY鍵、OpenSSH証明書、SSHエージェント、パスワードなしのログインに対応
  - 階層に制限のないグループ、ドラッグでの整理、複数選択と遅延測定
  - ホストごとのプロバイダー、更新周期、メモの記録と、更新前のリマインダー
  - ローカルでの認証情報の暗号化保存、鍵生成、公開鍵のインストール。ホスト鍵が変わったときはフィンガープリントで確認し、既知のホストも管理可能

- **バックアップと復元**
  - ホスト、認証情報、スニペット、設定をパスワードで暗号化してバックアップし、別のデバイスで復元
  - ローカル、WebDAV、S3互換ストレージ、シークレットGitHub Gist、またはiCloud Drive・OneDrive・Dropboxなどの同期フォルダーに保存
  - 手動・定期バックアップと、保持数の設定

- **ネットワーク**
  - 多段の踏み台ホストとHTTP / SOCKS5プロキシ。AIプロバイダーと更新確認でもプロキシを利用可能
  - ローカル、リモート、動的ポート転送

- **日々の使いやすさ**
  - サーバーのメトリクスをひと目で確認
  - ライト・ダークテーマ、背景画像、半透明表示、インターフェースの拡大縮小
  - トレイに閉じて、トレイのポップオーバーからホストにすぐ接続
  - 英語、簡体字中国語、繁体字中国語、韓国語、日本語に対応

## Linuxの認証情報と依存関係

SSHYardはrootや`sudo`を使わず、通常のデスクトップユーザーとして実行してください。認証情報の保存とロック解除には、libsecret 0.19以降（`libsecret-1.so.0`）、現在のユーザーのセッションD-Bus、そして利用可能でロック解除された[Secret Service](https://specifications.freedesktop.org/secret-service/latest/)プロバイダー（デスクトップに設定済みのGNOME Keyringやその他の互換プロバイダーなど）が必要です。libsecretをインストールしただけでは、実行中のSecret Serviceは用意されません。システムのサービスにはSSHYardのマスターキーだけが保存され、認証情報はローカルの`secrets.enc`に暗号化されたまま残ります。

- **Debian / Ubuntu：** `.deb`はGTKに加えて`libsecret-1-0 (>= 0.19)`を依存関係として宣言しています。`sudo apt install ./sshyard_<version>_amd64.deb`のように依存関係を解決できるパッケージマネージャーでインストールし、ファイル名は実際にダウンロードしたものに置き換えてください
- **Arch Linux：** 生成される`PKGBUILD`は`libsecret`と`gtk3`を宣言します。MyGoのDebianからArchへの対応付けではバージョン制約が外れるため、libsecretが0.19以降であることを確認してください
- **アーカイブ / `install.sh`：** ユーザーごとに`~/.local`へインストールし、libsecret、GTK、Secret Serviceプロバイダーは自動でインストールしません。先に依存関係を用意し、スクリプトをrootで実行しないでください

システムパッケージが不足している場合や、テナントのポリシーでセッションD-Bus / Secret Serviceが無効になっている場合は、管理者による対応が必要になることがあります。コレクションのロック解除が必要なときは、デスクトップの既存のキーリングマネージャーを使ってください。SSHYardはネイティブUIを使うため、WebKitGTKは不要です。

管理者権限のないテナントアカウントなどで利用できるSecret Serviceがない場合、SSHYardは代わりにローカルマスターパスワードでマスター鍵を保護できます。ロック解除に失敗し、まだ何も保存していないときに、エラーダイアログで**ローカルマスターパスワードを使用…**を選んでください。マスター鍵はそのパスワード（Argon2id）で封印されて`secrets.enc`の隣の`master.enc`に保存され、システムキーチェーンには何も書き込まれません。起動するたびに、保存された機密情報が最初に必要になったときにパスワードを入力します。SSHYardはパスワードを保存しないため、忘れた場合、保存された機密情報はバックアップからのみ復元できます。**設定 → このアプリについて → 秘密情報の保存先**でパスワードを変更したり、システムキーチェーンが使えるようになったらマスター鍵を戻したりできます。同じダイアログの**診断情報をコピー**は、問題の報告用に資格情報サービス自身のエラーメッセージをコピーします。機密情報は含まれません。

読み取り専用の接続確認は、SSHYardと**同じデスクトップのログインセッション**のターミナルから、`sudo`を付けずに実行してください（`gdbus`が必要です）。

```sh
gdbus call --session --dest org.freedesktop.secrets \
  --object-path /org/freedesktop/secrets \
  --method org.freedesktop.Secret.Service.ReadAlias default
```

コレクションのパスが返ればサービスに到達でき、`/`が返ればデフォルトのコレクションがありません。コレクションのパスが返った場合は、下の`/returned/collection/path`をそのパスに置き換えるとロック状態を確認できます。

```sh
gdbus call --session --dest org.freedesktop.secrets \
  --object-path /returned/collection/path \
  --method org.freedesktop.DBus.Properties.Get org.freedesktop.Secret.Collection Locked
```

`true`はロック中、`false`はロック解除済みを意味します。これらの呼び出しはセッションサービスを起動することがありますが、パスワードの読み取り、コレクションのロック解除、キーリング項目の変更・削除は行わず、接続とコレクションの状態を確認するだけです。コレクションがロック解除されていても、SSHYardがマスターキーを読み書きできるとは限りません。D-Busの接続エラーはログインセッションやアクセスポリシーの問題を示し、`ServiceUnknown`は通常、起動できるSecret Serviceプロバイダーがないことを意味します。戻り値については[ReadAlias API](https://specifications.freedesktop.org/secret-service/latest/org.freedesktop.Secret.Service.html#org.freedesktop.Secret.Service.ReadAlias)を参照してください。

## 開発

[mygo](https://github.com/egoist/mygo)を使用しています。Go 1.27.3が必要です（`GOTOOLCHAIN`でダウンロード可能）。cgoは不要です。

```sh
# 実行
go run .

# 開発用アプリを実行、macOSでは「Apple Development」の署名IDを推奨
# 安定した署名によりキーチェーンの確認を減らせる場合があります
# 自己署名では再ビルド後のアクセスを保証できず、macOSが再度認証を求めることもあります
scripts/run-dev-app.sh

# データを別のディレクトリに保存
SSHYARD_DATA_DIR=/tmp/demo go run .

# テスト用のローカルSSHサーバー（macOS / Linux）
go run ./cmd/devsshd -addr 127.0.0.1:2222 -user demo -password demo -root /tmp/remote

# テスト用のローカルHTTP / SOCKSプロキシ
go run ./cmd/devproxy -addr 127.0.0.1:3128

# テストとチェック
go test -race -count=1 ./...
go vet ./...
gofmt -l .

# locales/*.jsonの編集後に言語カタログを再生成
GOFLAGS=-mod=mod go run github.com/egoist/mygo/cmd/mygo generate

# dist/にパッケージを作成
go tool mygo build -platform linux/amd64,linux/arm64,windows/amd64,windows/arm64,darwin/arm64,darwin/amd64 -o dist
```

devsshdで試すには、ホスト`127.0.0.1`、ポート`2222`、ユーザー`demo`、パスワード`demo`を追加してください。

コントリビューター向けのルールは[AGENTS.md](AGENTS.md)をご覧ください。

## コミュニティサポート

- [LinuxDO](https://linux.do)

## ライセンス

SSHYardは[GNU Affero General Public License v3.0](LICENSE)のもとで提供されています。
