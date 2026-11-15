<p align="left"><a href="README.md">English</a> · <a href="README_ZH.md">简体中文</a> · <a href="README_ZH_TW.md">繁體中文</a> · <strong>한국어</strong> · <a href="README_JA.md">日本語</a></p>

<p align="center">
  <img src="resources/icon.png" alt="SSHYard 로고" width="128" height="128">
</p>

<h1 align="center">SSHYard</h1>

SSHYard는 macOS, Windows 및 Linux용 네이티브 데스크톱 SSH / SFTP 클라이언트로, 터미널과 파일 관리, AI 도우미를 하나의 앱에서 제공합니다.

## 스크린샷

<table>
  <tr>
    <td width="50%" align="center">
      <img src="docs/terminal-ai-assistant.webp" alt="터미널 및 AI 도우미" width="100%"><br>
      터미널 및 AI 도우미
    </td>
    <td width="50%" align="center">
      <img src="docs/sftp-file-manager.webp" alt="SFTP 파일 관리자" width="100%"><br>
      SFTP 파일 관리자
    </td>
  </tr>
  <tr>
    <td width="50%" align="center">
      <img src="docs/system-monitor.webp" alt="시스템 모니터" width="100%"><br>
      시스템 모니터
    </td>
    <td width="50%" align="center">
      <img src="docs/ai-assistant-settings.webp" alt="AI 도우미 설정" width="100%"><br>
      AI 도우미 설정
    </td>
  </tr>
</table>

## 주요 기능

- **터미널**
  - 탭, 분할 창 및 터미널 검색, 탭 순서 변경 및 이름 바꾸기
  - 트루 컬러, 전각 문자, 입력기와 tmux, vim, htop의 마우스 조작 지원
  - 내장 색 구성표 10종과 `.itermcolors` 가져오기, 키워드 강조, 줄 번호와 타임스탬프, 커서·줄 높이·글자 폭 조절
  - 다양한 출력 인코딩을 호스트별로 설정, ⌘ / Ctrl + 클릭으로 터미널의 링크 열기
  - SSH 외에 Telnet, 직렬 포트 및 로컬 터미널 지원(Windows에서는 PowerShell, CMD, Git Bash, WSL 선택)
  - 같은 호스트의 여러 창에서 연결을 공유하고 자동으로 재연결

- **작업 공간**
  - 시스템 모니터, 프로세스 사용량, 호스트 상세 정보, Docker, tmux, 스니펫, 명령 기록, 명령 브로드캐스트, 세션 로그를 끌어서 옮길 수 있는 창으로 열어 아무 창 옆이나 다른 탭에 배치
  - 재연결 후 원래 세션으로 돌아가는 tmux 세션 관리
  - Docker 컨테이너와 이미지의 상세 정보 및 리소스 사용량 확인, 컨테이너 시작·중지
  - 입력 동기화를 지원하는 명령 브로드캐스트, 텍스트 또는 원본 형식의 세션 로그
  - 명령 팔레트와 사용자 지정 단축키, 창과 분할 레이아웃 기억

- **파일 관리**
  - 터미널 옆에서 SFTP 사용, 디렉터리 따라가기, 서버에 SFTP가 없으면 SCP로 자동 전환
  - 디렉터리 트리를 갖춘 탭형 내장 편집기
  - 필요할 때 sudo로 권한을 높여 파일 접근
  - 드래그 앤 드롭과 폴더 전송 지원, 업로드 시 권한과 수정 시간 유지
  - 드래그 앤 드롭이나 복사·붙여넣기로 서버 간에 파일과 폴더를 직접 복사, 데이터는 앱 메모리를 거치며 로컬 디스크에 쓰지 않음
  - 일시 정지, 재개 및 다시 시도가 가능한 전송 대기열

- **AI 도우미**
  - 호스트별 전용 도우미로 명령 실행 및 파일 편집, SSH·Telnet·직렬 포트·로컬 터미널 모두 지원, 대화는 호스트별로 저장
  - AI 제공자 연결 또는 이 컴퓨터에서 자동으로 찾은 ACP 에이전트(Claude Code, Codex, Grok, Gemini CLI 등) 사용. 여러 개를 함께 추가하고 각 에이전트가 지원하는 모델과 사고 수준을 고를 수 있으며, 작업은 원격 호스트에서만 이루어집니다
  - 이미지와 파일을 붙여 넣어 첨부
  - 묻기, 검토 또는 전체 접근 모드 선택, 검토 모델·턴당 도구 호출 한도·요청 시간 제한 설정

- **에이전트 연동**
  - 공개 API, MCP 및 스킬을 통해 로컬 AI 에이전트 연결
  - 저장된 자격 증명을 노출하지 않고 호스트 탐색, 연결 및 명령 실행
  - Claude Code와 Codex용 스킬을 한 번의 클릭으로 설정, 에이전트 접근은 기본적으로 꺼짐

- **호스트 및 자격 증명**
  - SSH 설정에서 가져오기, Xshell, MobaXterm, PuTTY 등 10종의 클라이언트에서 마이그레이션, 텍스트로 일괄 가져오기 또는 `user@host:port`로 빠르게 연결
  - 비밀번호, OpenSSH / PEM / PuTTY 키, OpenSSH 인증서 및 SSH 에이전트 지원
  - 단계 제한 없는 그룹, 드래그로 정리, 다중 선택과 응답 속도 측정
  - 호스트별 제공업체, 갱신 주기 및 메모 기록과 갱신 전 알림
  - 로컬 암호화 자격 증명 저장소, 키 생성 및 공개 키 설치, 호스트 키가 바뀌면 지문을 확인하고 알려진 호스트 관리

- **백업 및 복원**
  - 호스트, 자격 증명, 스니펫 및 설정을 비밀번호로 암호화하여 백업하고 다른 기기에서 복원
  - 로컬, WebDAV, S3 호환 스토리지 및 비밀 GitHub Gist에 저장
  - 수동 또는 예약 백업과 보관 개수 설정

- **네트워크**
  - 여러 홉의 점프 호스트 및 HTTP / SOCKS5 프록시, AI 제공자와 업데이트 확인에도 프록시 사용 가능
  - 로컬, 원격 및 동적 포트 포워딩

- **일상적인 사용**
  - 서버 지표를 한눈에 확인
  - 라이트 및 다크 테마, 배경 이미지, 반투명 효과 및 인터페이스 배율
  - 트레이로 닫고 트레이 팝업에서 호스트에 바로 연결
  - 영어, 중국어 간체, 중국어 번체, 한국어 및 일본어 지원

## Linux 자격 증명과 의존성

SSHYard는 root나 `sudo` 없이 일반 데스크톱 사용자로 실행하세요. 자격 증명을 저장하고 잠금 해제하려면 libsecret 0.19 이상(`libsecret-1.so.0`), 현재 사용자의 세션 D-Bus, 그리고 사용 가능하고 잠금 해제된 [Secret Service](https://specifications.freedesktop.org/secret-service/latest/) 제공자(데스크톱에 설정된 GNOME Keyring 또는 다른 호환 제공자 등)가 필요합니다. libsecret만 설치해서는 실행 중인 Secret Service가 제공되지 않습니다. 시스템 서비스에는 SSHYard의 마스터 키만 저장되며, 자격 증명은 로컬 `secrets.enc`에 암호화된 채로 남습니다.

- **Debian / Ubuntu:** `.deb`는 GTK와 함께 `libsecret-1-0 (>= 0.19)`를 의존성으로 선언합니다. `sudo apt install ./sshyard_<version>_amd64.deb`처럼 의존성을 처리하는 패키지 관리자로 설치하고, 파일 이름은 실제로 내려받은 이름을 사용하세요
- **Arch Linux:** 생성된 `PKGBUILD`는 `libsecret`과 `gtk3`를 선언합니다. MyGo의 Debian→Arch 매핑은 버전 제약을 없애므로 libsecret이 0.19 이상인지 직접 확인하세요
- **압축 파일 / `install.sh`:** 사용자별로 `~/.local`에 설치하며 libsecret, GTK, Secret Service 제공자를 자동으로 설치하지 않습니다. 의존성을 먼저 준비하고 스크립트를 root로 실행하지 마세요

시스템 패키지가 없거나 테넌트 정책으로 세션 D-Bus / Secret Service가 비활성화된 경우 관리자의 조치가 필요할 수 있습니다. 컬렉션 잠금 해제가 필요하면 데스크톱의 기존 키링 관리자를 사용하세요. SSHYard는 네이티브 UI를 사용하므로 WebKitGTK가 필요하지 않습니다.

관리자 권한이 없는 테넌트 계정처럼 사용할 수 있는 Secret Service가 없으면, SSHYard는 대신 로컬 마스터 비밀번호로 마스터 키를 보호할 수 있습니다. 잠금 해제에 실패했고 아직 저장된 항목이 없을 때 오류 대화상자에서 **로컬 마스터 비밀번호 사용…**을 선택하세요. 마스터 키는 그 비밀번호(Argon2id)로 봉인되어 `secrets.enc` 옆의 `master.enc`에 저장되고, 시스템 키체인에는 아무것도 기록되지 않으며, 실행할 때마다 저장된 비밀 정보가 처음 필요할 때 비밀번호를 입력합니다. SSHYard는 비밀번호를 저장하지 않으므로 잊으면 저장된 비밀 정보는 백업에서만 복구할 수 있습니다. **설정 → 정보 → 비밀 정보 저장소**에서 비밀번호를 변경하거나, 시스템 키체인을 사용할 수 있게 되면 마스터 키를 다시 옮길 수 있습니다. 같은 대화상자의 **진단 정보 복사**는 문제 보고용으로 자격 증명 서비스 자체의 오류 메시지를 복사하며, 비밀 정보는 포함되지 않습니다.

읽기 전용 연결 확인은 SSHYard와 **같은 데스크톱 로그인 세션**의 터미널에서 `sudo` 없이 실행하세요(`gdbus` 필요).

```sh
gdbus call --session --dest org.freedesktop.secrets \
  --object-path /org/freedesktop/secrets \
  --method org.freedesktop.Secret.Service.ReadAlias default
```

컬렉션 경로가 반환되면 서비스에 연결할 수 있다는 뜻이고, `/`가 반환되면 기본 컬렉션이 없다는 뜻입니다. 컬렉션 경로가 반환되었다면 아래의 `/returned/collection/path`를 그 경로로 바꿔 잠금 상태를 확인할 수 있습니다.

```sh
gdbus call --session --dest org.freedesktop.secrets \
  --object-path /returned/collection/path \
  --method org.freedesktop.DBus.Properties.Get org.freedesktop.Secret.Collection Locked
```

`true`는 잠김, `false`는 잠금 해제를 뜻합니다. 이 호출은 세션 서비스를 활성화할 수 있지만 비밀번호를 읽거나 컬렉션을 잠금 해제하거나 키링 항목을 수정·삭제하지 않으며, 연결과 컬렉션 상태만 확인합니다. 컬렉션이 잠금 해제되어 있어도 SSHYard가 마스터 키를 읽거나 저장할 수 있다고 보장되지는 않습니다. D-Bus 연결 오류는 로그인 세션이나 접근 정책 문제를 가리키며, `ServiceUnknown`은 대개 활성화할 수 있는 Secret Service 제공자가 없다는 뜻입니다. 반환 값은 [ReadAlias API](https://specifications.freedesktop.org/secret-service/latest/org.freedesktop.Secret.Service.html#org.freedesktop.Secret.Service.ReadAlias)를 참고하세요.

## 개발

[mygo](https://github.com/egoist/mygo)로 개발했습니다. Go 1.27.3이 필요하며 `GOTOOLCHAIN`으로 다운로드할 수 있습니다. cgo는 필요하지 않습니다.

```sh
# 실행
go run .

# 개발 앱 실행, macOS에서는 "Apple Development" 서명 인증서 권장
# 일관된 서명은 키체인 승인 요청을 줄일 수 있지만 자체 서명은
# 재빌드 후의 접근을 보장하지 않으며 macOS가 다시 승인을 요청할 수 있음
scripts/run-dev-app.sh

# 데이터를 별도 디렉터리에 보관
SSHYARD_DATA_DIR=/tmp/demo go run .

# 테스트용 로컬 SSH 서버(macOS / Linux)
go run ./cmd/devsshd -addr 127.0.0.1:2222 -user demo -password demo -root /tmp/remote

# 테스트용 로컬 HTTP / SOCKS 프록시
go run ./cmd/devproxy -addr 127.0.0.1:3128

# 테스트 및 검사
go test -race -count=1 ./...
go vet ./...
gofmt -l .

# locales/*.json 편집 후 로케일 카탈로그 다시 생성
GOFLAGS=-mod=mod go run github.com/egoist/mygo/cmd/mygo generate

# dist/에 패키징
go tool mygo build -platform linux/amd64,linux/arm64,windows/amd64,windows/arm64,darwin/arm64,darwin/amd64 -o dist
```

devsshd로 테스트하려면 호스트 `127.0.0.1`, 포트 `2222`, 사용자 `demo`, 비밀번호 `demo`를 추가하세요.

기여자 규칙은 [AGENTS.md](AGENTS.md)를 참고하세요.

## 커뮤니티 지원

- [LinuxDO](https://linux.do)

## 라이선스

SSHYard는 [GNU Affero General Public License v3.0](LICENSE)으로 배포됩니다.
