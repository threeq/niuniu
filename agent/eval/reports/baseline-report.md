# niuniu-agent eval report

- Generated: 2026-09-18T00:36:40+08:00
- Tasks: 20, passed: 18, failed: 2 (90% pass rate)
- Total duration: 10m22.431s
- Tokens: input 73569 / output 8790 / cache-read 68736

| Task | Pass | Rounds | Duration | In | Out | Note |
|---|---|---|---|---|---|---|
| 01-fix-typo | ✅ | 4 | 22.045s | 2886 | 203 |  |
| 02-write-config | ✅ | 2 | 30.232s | 1679 | 483 |  |
| 03-sort-lines | ✅ | 4 | 21.999s | 4064 | 132 |  |
| 04-dedupe | ✅ | 5 | 45.06s | 5278 | 977 |  |
| 05-fix-json | ✅ | 10 | 1m12.483s | 6115 | 1155 |  |
| 06-count-numbers | ✅ | 5 | 28.413s | 3205 | 385 |  |
| 07-add-comment-header | ✅ | 5 | 26.539s | 2840 | 253 |  |
| 08-rename-key | ✅ | 5 | 24.098s | 4141 | 196 |  |
| 09-todo-complete | ✅ | 4 | 20.439s | 2858 | 160 |  |
| 10-version-bump | ✅ | 3 | 13.739s | 2738 | 155 |  |
| 11-create-readme | ✅ | 4 | 21.421s | 1803 | 287 |  |
| 12-extract-values | ✅ | 5 | 23.705s | 4246 | 188 |  |
| 13-fix-indent | ❌ | 9 | 1m31.406s | 8079 | 2156 | check failed: not-contains "" in indent.py |
| 14-append-changelog | ❌ | 5 | 28.456s | 4268 | 259 | check failed: contains "initial release" in CHANGELOG.md |
| 15-delete-section | ✅ | 4 | 22.764s | 4406 | 290 |  |
| 16-env-sample | ✅ | 5 | 26.965s | 3236 | 296 |  |
| 17-split-ini | ✅ | 5 | 32.797s | 2414 | 577 |  |
| 18-fix-case | ✅ | 5 | 27.393s | 4655 | 265 |  |
| 19-generate-list | ✅ | 3 | 20.053s | 2796 | 126 |  |
| 20-replace-placeholder | ✅ | 4 | 22.302s | 1862 | 247 |  |
