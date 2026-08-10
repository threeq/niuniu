# Repository Detail Page Specification

## Overview

A dedicated repository detail page (`/repositories/:id`) providing comprehensive repository management with tabbed navigation for Files, Branches, Commits, Worktrees, and Settings.

---

## Page Structure

```
┌─────────────────────────────────────────────────────────────────┐
│  Header: [← Back] Repo Name  [Default Branch]  [Refresh] [⚙]  │
├─────────────────────────────────────────────────────────────────┤
│  Tabs: [Files] [Branches] [Commits] [Worktrees] [Settings]      │
├─────────────────────────────────────────────────────────────────┤
│                                                                 │
│  Tab Content Area (varies by selected tab)                      │
│                                                                 │
│                                                                 │
└─────────────────────────────────────────────────────────────────┘
```

---

## Tab 1: Files

### Purpose
Browse repository file tree and view file contents.

### Content
- **Breadcrumb**: Shows current path (e.g., `root / src / components`)
- **File Tree**: Collapsible tree structure with folders and files
- **File Preview**: Click file to preview in read-only editor panel

### Operations
| Action | Trigger | Result |
|--------|---------|--------|
| Expand folder | Click folder | Shows children |
| Collapse folder | Click expanded folder | Hides children |
| Preview file | Click file | Opens in center panel |
| Copy path | Right-click → Copy Path | Copies full path to clipboard |

### Layout
```
┌────────────────┬────────────────────────────────────────┐
│ File Tree      │ File Preview (Monaco/CodeMirror)      │
│ (scrollable)   │ (read-only, syntax highlighted)       │
│                │                                        │
│ 📁 src/        │ // component code                     │
│   📁 components│ function Hello() {...}               │
│     Button.tsx │                                        │
│   App.tsx      │                                        │
└────────────────┴────────────────────────────────────────┘
```

---

## Tab 2: Branches

### Purpose
View all branches, switch branches, create/delete branches.

### Content
- **Branch List**: Scrollable list showing all branches
- **Current Branch**: Highlighted with blue background
- **Branch Actions**: Per-row action buttons

### Operations
| Action | Trigger | Result |
|--------|---------|--------|
| Switch branch | Click branch row | Updates HEAD to selected branch |
| Create branch | Click "+ New Branch" → Enter name | Creates and switches to new branch |
| Delete branch | Click trash icon → Confirm | Deletes local branch |
| Copy branch name | Click copy icon | Copies branch name to clipboard |

### API Endpoints Needed
- `GET /repositories/:id/branches` - List branches
- `POST /repositories/:id/branches` - Create branch
- `DELETE /repositories/:id/branches/:name` - Delete branch
- `PUT /repositories/:id/branches/:name/checkout` - Switch branch

### Layout
```
┌─────────────────────────────────────────────────────────────┐
│ Branches                              [+ New Branch]         │
├─────────────────────────────────────────────────────────────┤
│ ● main                                        [⎘] [🗑]     │
│   feature/login                              [⎘] [🗑]     │
│   bugfix/header                              [⎘] [🗑]     │
│                                                             │
└─────────────────────────────────────────────────────────────┘
```
Legend: ● = current branch, [⎘] = copy, [🗑] = delete

---

## Tab 3: Commits

### Purpose
View commit history with pagination.

### Content
- **Commit List**: Paginated list (20 per page)
- **Commit Info**: Hash (short), author, date, message
- **Pagination**: Previous/Next buttons with page indicator

### Operations
| Action | Trigger | Result |
|--------|---------|--------|
| View commit | Click commit row | Expands to show diff/stats |
| Load more | Click "Load More" or paginate | Fetches next page |
| Copy commit hash | Click hash | Copies full hash to clipboard |

### API Endpoints Needed
- `GET /repositories/:id/commits?page=1&limit=20` - List commits with pagination
- `GET /repositories/:id/commits/:hash` - Get commit detail

### Layout
```
┌─────────────────────────────────────────────────────────────┐
│ Commits                                        Page 1 of 5  │
├─────────────────────────────────────────────────────────────┤
│ a1b2c3d Fix login button styling              2h ago  │
│ e4f5g6h Add user profile component             5h ago  │
│ i7j8k9l Merge branch 'main'                   Yesterday│
│ ...                                                          │
│                     [← Previous] [Next →]                    │
└─────────────────────────────────────────────────────────────┘
```

---

## Tab 4: Worktrees

### Purpose
Manage git worktrees for the repository.

### Content
- **Worktree List**: Shows all worktrees with branch and path
- **Worktree Status**: Indicates if worktree has uncommitted changes

### Operations
| Action | Trigger | Result |
|--------|---------|--------|
| Create worktree | Click "+ New Worktree" → Fill form | Creates new worktree |
| Remove worktree | Click trash icon → Confirm | Removes worktree (not files) |
| Open in new tab | Click open icon | Opens worktree path in new browser tab |
| Copy path | Click copy icon | Copies worktree path |

### API Endpoints Needed
- `GET /repositories/:id/worktrees` - List worktrees
- `POST /repositories/:id/worktrees` - Create worktree
- `DELETE /repositories/:id/worktrees/:path` - Remove worktree

### Layout
```
┌─────────────────────────────────────────────────────────────┐
│ Worktrees                              [+ New Worktree]     │
├─────────────────────────────────────────────────────────────┤
│ 📁 feature/auth              auth        ~/niuniu/auth  [!] │
│ 📁 feature/dashboard         dashboard   ~/niuniu/dash  [ ]│
│                                                             │
└─────────────────────────────────────────────────────────────┘
Legend: [!] = has uncommitted changes, [ ] = clean
```

---

## Tab 5: Settings

### Purpose
Configure repository settings.

### Content
- **Repository Name**: Editable name
- **Repository Path**: Read-only path on disk
- **Remote URL**: Git remote URL (fetch/push)
- **Default Branch**: Dropdown to select default branch
- **Delete Repository**: Danger zone to remove from system

### Operations
| Action | Trigger | Result |
|--------|---------|--------|
| Update name | Edit name → Save | Updates repository name |
| Update remote | Edit remote URL → Save | Updates git remote |
| Change default branch | Select from dropdown | Updates default branch |
| Delete repository | Click delete → Confirm | Removes from system (optionally deletes files) |

### API Endpoints (Existing)
- `PUT /repositories/:id` - Update repository
- `DELETE /repositories/:id?delete_directory=false` - Delete repository

### Layout
```
┌─────────────────────────────────────────────────────────────┐
│ Settings                                                      │
├─────────────────────────────────────────────────────────────┤
│ Name                     [________________] [Save]           │
│ Path                     /home/user/projects/myrepo  [📋]   │
│ Remote URL               [________________] [Save]           │
│ Default Branch           [main ▼]                [Save]      │
│                                                              │
│ ─────────────────────────────────────────────────────────────│
│ ⚠ Danger Zone                                                │
│ [Delete Repository] (removes from system, keeps files)      │
│ [Delete Repository & Files] (removes everything)            │
└─────────────────────────────────────────────────────────────┘
```

---

## Header Bar

### Content
- **Back Button**: Returns to previous page (repositories list or dashboard)
- **Repository Name**: Displays current repo name
- **Default Branch Badge**: Shows default branch name
- **Refresh Button**: Reloads current tab data
- **Settings Gear**: Navigates to Settings tab

### Layout
```
┌─────────────────────────────────────────────────────────────┐
│ [←]  my-repo                              main  [↻] [⚙]   │
└─────────────────────────────────────────────────────────────┘
```

---

## Backend API Requirements

### New Endpoints

| Method | Endpoint | Description |
|--------|----------|-------------|
| GET | `/repositories/:id/files` | List files at path (query: `?path=/src`) |
| GET | `/repositories/:id/files/*path` | Get file content |
| GET | `/repositories/:id/commits` | List commits (query: `?page=1&limit=20`) |
| GET | `/repositories/:id/commits/:hash` | Get commit detail |
| POST | `/repositories/:id/branches` | Create branch (body: `{"name":"feature/x"}`) |
| DELETE | `/repositories/:id/branches/:name` | Delete branch |
| PUT | `/repositories/:id/branches/:name/checkout` | Checkout branch |
| GET | `/repositories/:id/worktrees` | List worktrees |
| POST | `/repositories/:id/worktrees` | Create worktree |
| DELETE | `/repositories/:id/worktrees/:worktree_path` | Remove worktree |
| GET | `/repositories/:id/stats` | Get repo stats (total commits, branches, contributors) |

### Repository Stats Object
```typescript
interface RepositoryStats {
  total_commits: number;
  total_branches: number;
  total_contributors: number;
  open_issues?: number;  // future
  last_commit_date: string;
}
```

---

## Frontend Components

### New Components
- `pages/repositories/repo-detail.tsx` - Main page component
- `components/repo-tabs.tsx` - Tab navigation
- `components/repo-files-tab.tsx` - Files browser
- `components/repo-branches-tab.tsx` - Branch management
- `components/repo-commits-tab.tsx` - Commit history
- `components/repo-worktrees-tab.tsx` - Worktree management
- `components/repo-settings-tab.tsx` - Repository settings

### Routing
```
/repositories/:id  →  RepoDetailPage
```

---

## Implementation Priority

1. **Backend API**: Commits, Files, Stats endpoints
2. **Frontend Page Structure**: Route, Tabs, Header
3. **Files Tab**: File tree + preview
4. **Branches Tab**: List, create, delete
5. **Commits Tab**: List with pagination
6. **Worktrees Tab**: List, create, delete
7. **Settings Tab**: Update/Delete operations
