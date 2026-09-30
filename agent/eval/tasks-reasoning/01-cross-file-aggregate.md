---
name: reasoning-cross-file-aggregate
description: Multi-step tool chain — read three data files, aggregate, write a report
---
## Task
The files region-north.csv, region-south.csv and region-east.csv each contain lines of the form `item,amount`. Read ALL three files, compute the grand total of all amounts across them, and write it to report.txt as exactly one line: `total=<number>` (no spaces, no thousands separators).

## Fixtures
- region-north.csv: alpha,100\nbeta,250\ngamma,50
- region-south.csv: alpha,300\ndelta,150
- region-east.csv: beta,600\nepsilon,900

## Checks
- file-exists: report.txt
- contains: report.txt, total=2350
