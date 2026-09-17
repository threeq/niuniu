---
name: 15-delete-section
description: 删除指定段落
---

## Task
doc.md 中有一行以 'DEPRECATED:' 开头，把整行删除，其余内容保持。

## Fixtures
- doc.md: # Guide

DEPRECATED: old api

## Usage

Run it.


## Checks
- not-contains: doc.md, DEPRECATED
- contains: doc.md, ## Usage
