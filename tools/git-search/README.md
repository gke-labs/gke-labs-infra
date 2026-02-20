# git-search

`git-search` is a tool that allows searching for a regex in a remote git repository without needing to manually clone and manage temporary checkouts.

## Usage

```bash
git-search --repo <repository-url> [--ref <ref>] <regex>
```

### Examples

Search for "needle" in the main branch of a repo:
```bash
git-search --repo https://github.com/foo/bar needle
```

Search for "needle" in a specific branch:
```bash
git-search --repo https://github.com/foo/bar --ref develop needle
```

## How it works

1. It creates a bare clone of the repository in a local cache directory (typically `~/.cache/git-search/repos`).
2. It uses `git grep` to search for the regex directly within the git objects of the requested ref.

Subsequent searches against the same repository will reuse the bare clone, only fetching updates if necessary.
