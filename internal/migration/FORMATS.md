# Import format scope

Parsers read exports locally without executing registry files, connecting to a host,
following directory symlinks, or opening private-key paths; credential bytes remain
in memory and are excluded from JSON/YAML serialization.

`Read` limits selected input to 1,024 unique regular files, 32 MiB total and 10,000
SSH entries and 10,000 diagnostics; a file error rejects the batch; non-SSH rows and unsupported options
produce machine-readable diagnostics; unknown or damaged schemas fail explicitly.
A directory contributes only its relative subdirectory path to group names.
A malformed INI line fails its file with the file name and line number.

Exports of Xshell, MobaXterm, PuTTY, Remmina, mRemoteNG and WinSCP that are
neither UTF-8 nor UTF-16 with a byte order mark are in a legacy code page: the
one chosen in the preview, else the first that decodes them cleanly among the
Windows ANSI code page, the interface language's (GB18030, Big5, Shift_JIS,
EUC-KR, else Windows-1252) and then those in that order. A MobaXterm bookmark
that cannot be read is skipped with its line number while the others import,
and `<default>` users take `DefaultLoginName` from the same file.
`ParseText` keeps the same size, entry and diagnostic bounds for typed text.

## Text host list

`Text` reads one host per line of blank-separated `key=value` fields, typed or
pasted into the text area or read from a selected `.txt` file: `host` / `ip`,
`port`, `user` / `username`, `password` and `title` / `name` / `label` for the
name. Keys ignore case; `authType` is ignored, other keys are diagnosed, and a
missing password stays blank. A value opening with `"` or `'` runs to the
matching quote, where in double quotes `\"` and `\\` escape; an unquoted value is
literal up to the next blank, quotes and backslashes included, and an unclosed
quote is diagnosed. Blank lines, `#` comments and lines without a field, such
as separators, are skipped. The preview edits every field and the login of each
row, alone or for the selected rows at once, and saves only once every
selected row is valid.

## CSV host table

`CSV` reads a selected `.csv` file whose header row names the columns with the
text host list's keys, under the same rules: one host per row, each cell a
`key=value` field under its column name, a blank cell as no field, other columns
diagnosed only where a row fills them. Columns split on commas, or on semicolons
or tabs when the header has no comma; quoting follows CSV, a stray quote stays
literal, and cells other than `password` are trimmed. Rows without a value and
`#` comments are skipped; a header without a `host` or `ip` column fails as an
unsupported format. The preview is the text import's.

## Independently verified source formats

- WindTerm: `user.sessions` JSON array, public `session.target`, `session.label`,
  `session.folder` / `session.group`, `session.port`, `session.protocol` and
  `session.uuid` fields; target userinfo and literal IPv6 are separated locally;
  hidden/private credential payloads are diagnosed, never decoded or treated as
  passwords; no claim is made to decrypt WindTerm profile data
  - [Upstream format discussion](https://github.com/kingToolbox/WindTerm/issues/533)
  - [Upstream credential-protection description](https://kingtoolbox.github.io/2021/03/11/protection-username-password/)
- Electerm: bookmarks JSON array or `{bookmarks, bookmarkGroups}`, group ancestry
  from `bookmarkGroupIds` and bookmark membership from `bookmarkIds`; readable
  password/privateKey/passphrase fields and `privateKeyPath`; `passwordEncrypted`
  is diagnosed without decoding; profile-based auth, connection scripts and
  unsupported proxy configuration are reported
  - [Bookmark schema, snapshot 3db5ec7](https://github.com/electerm/electerm/blob/3db5ec7d9a2d3833d89a7c24978e1c77526e145a/src/client/common/bookmark-schemas.js)
  - [Group tree](https://github.com/electerm/electerm/blob/3db5ec7d9a2d3833d89a7c24978e1c77526e145a/src/client/common/bookmark-group-tree.js)
  - [Password flag](https://github.com/electerm/electerm/blob/3db5ec7d9a2d3833d89a7c24978e1c77526e145a/src/client/common/pass-enc.js)
- Tabby: YAML `profiles` of type `ssh`; global `profileDefaults.ssh`, then the
  containing group's `defaults.ssh`, then profile options; group path uses
  `parentGroupId`; current upstream applies only the containing group's defaults,
  not each ancestor's options; `privateKeys` paths and `jumpHost` IDs are retained;
  vault/keychain passwords are not decrypted; agent authentication, additional
  keys, proxy commands and port forwarding require manual setup
  - [Profile merge, snapshot 467ce8e](https://github.com/Eugeny/tabby/blob/467ce8ebfa467fb88cf6d154c9a5891e3d3647f0/tabby-core/src/services/profiles.service.ts)
  - [SSH options](https://github.com/Eugeny/tabby/blob/467ce8ebfa467fb88cf6d154c9a5891e3d3647f0/tabby-ssh/src/api/interfaces.ts)
  - [Credential storage](https://github.com/Eugeny/tabby/blob/467ce8ebfa467fb88cf6d154c9a5891e3d3647f0/tabby-ssh/src/services/passwordStorage.service.ts)
- Netcatty: metadata JSON `{hosts, keys?, identities?}` and exported CSV with
  `Groups,Label,Tags,Notes,Hostname/IP,Protocol,Port,Username,Password` plus optional
  `KeyPath,Passphrase,Proxy,ProxyJump,LocalShell,SshInputCompatibility`; CSV marked
  fields use URI decoding; JSON identity/key references and host-chain IDs are
  resolved; device-encrypted credential placeholders are distinguished by their
  actual platform headers and lengths; encrypted backup envelopes are rejected;
  tags, notes, proxy configuration and group connection defaults are diagnosed
  - [CSV export, snapshot 54c4db5](https://github.com/binaricat/Netcatty/blob/54c4db5030d421f665f2d98dcaae1fb8065b8256/domain/vaultImport/csvExport.ts)
  - [CSV field markers](https://github.com/binaricat/Netcatty/blob/54c4db5030d421f665f2d98dcaae1fb8065b8256/domain/vaultImport/csvCredentialFields.ts)
  - [Host/key/identity schema](https://github.com/binaricat/Netcatty/blob/54c4db5030d421f665f2d98dcaae1fb8065b8256/domain/models/connection.ts)
  - [Credential placeholders](https://github.com/binaricat/Netcatty/blob/54c4db5030d421f665f2d98dcaae1fb8065b8256/domain/credentials.ts)
  - [Authentication selection](https://github.com/binaricat/Netcatty/blob/54c4db5030d421f665f2d98dcaae1fb8065b8256/domain/sshAuth.ts)

These snapshots establish format coverage, not end-to-end acceptance against every
historical release or a user's actual encrypted profile; unsupported/versioned
backup envelopes, third-party extensions and external key-file readability still
need the user's source exports and explicit credential-saving choice.
