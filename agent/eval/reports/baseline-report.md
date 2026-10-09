# niuniu-agent eval report

- Generated: 2026-10-09T10:54:47+08:00
- Tasks: 27, passed: 26, failed: 1 (96% pass rate)
- Total duration: 3m27.985s
- Tokens: input 51093 / output 24595 / cache-read 364160

| Task | Pass | Rounds | Duration | In | Out | Note |
|---|---|---|---|---|---|---|
| 01-fix-typo | ✅ | 5 | 5.229s | 1015 | 479 |  |
| 02-write-config | ✅ | 3 | 2.165s | 563 | 186 |  |
| 03-sort-lines | ✅ | 6 | 6.384s | 1172 | 496 |  |
| 04-dedupe | ✅ | 7 | 10.966s | 1444 | 1294 |  |
| 05-fix-json | ✅ | 9 | 8.241s | 1662 | 662 |  |
| 06-count-numbers | ✅ | 7 | 7.403s | 1345 | 640 |  |
| 07-add-comment-header | ✅ | 6 | 6.966s | 1297 | 592 |  |
| 08-rename-key | ✅ | 5 | 4.352s | 945 | 468 |  |
| 09-todo-complete | ✅ | 5 | 3.912s | 1019 | 331 |  |
| 10-version-bump | ✅ | 4 | 2.633s | 770 | 231 |  |
| 11-create-readme | ✅ | 6 | 9.154s | 15129 | 1143 |  |
| 12-extract-values | ✅ | 10 | 18.358s | 5968 | 1982 |  |
| 13-fix-indent | ✅ | 6 | 5.602s | 1325 | 663 |  |
| 14-append-changelog | ❌ | 6 | 4.84s | 1276 | 513 | check failed: contains "initial release" in CHANGELOG.md |
| 15-delete-section | ✅ | 4 | 2.712s | 773 | 195 |  |
| 16-env-sample | ✅ | 4 | 2.562s | 842 | 269 |  |
| 17-split-ini | ✅ | 12 | 14.044s | 2440 | 1540 |  |
| 18-fix-case | ✅ | 5 | 4.304s | 980 | 451 |  |
| 19-generate-list | ✅ | 3 | 2.276s | 571 | 144 |  |
| 20-replace-placeholder | ✅ | 4 | 5.348s | 800 | 375 |  |
| 21-memory-state-vs-preference | ✅ | 2 | 4.598s | 343 | 805 |  |
| 22-memory-same-turn-correction | ✅ | 9 | 15.872s | 2053 | 2055 |  |
| 23-memory-correction-behavior-sync | ✅ | 13 | 16.936s | 2662 | 1794 |  |
| 24-memory-expired-stop-recall | ✅ | 2 | 5.959s | 301 | 955 |  |
| 25-memory-domain-classification | ✅ | 12 | 22.925s | 2714 | 4133 |  |
| 26-memory-open-item-revisit | ✅ | 2 | 3.658s | 332 | 589 |  |
| 27-memory-transient-state-not-persisted | ✅ | 7 | 10.295s | 1352 | 1610 |  |
