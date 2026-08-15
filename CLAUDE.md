# Claude Code Configuration

## Global Rules

### Git Commit Policy
- **NEVER automatically use `git commit` on your own**
- Always require explicit user consent before committing
- Only commit when the user explicitly requests it with phrases like:
  - "create a commit"
  - "commit these changes"
  - "make a commit"
  - etc.
- When user asks to commit, follow the Bash tool's git commit protocol:
  1. Run `git status` to see all changes
  2. Run `git diff` to see staged and unstaged changes
  3. Run `git log` to see recent commit messages for style consistency
  4. Draft a clear commit message based on the changes
  5. Use the HEREDOC format for multi-line commit messages
  6. Include the footer: "🤖 Generated with [Claude Code](https://claude.com/claude-code)"
  7. Include co-author line: "Co-Authored-By: Claude Sonnet 4.5 <noreply@anthropic.com>"

## Implementation Notes

This ensures all commits are intentional and user-approved, maintaining clear commit history and preventing unintended changes from being recorded.
