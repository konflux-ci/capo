# Capo AI Skills

Repository-specific AI skills for capo and buildprobe. These skills are tool-agnostic and can be used with any AI agent (Claude Code, Codex, Goose, etc.) via symlinks to the agent's skill directory.

## Available Skills

| Skill | Description |
|-------|-------------|
| [testing](testing/SKILL.md) | How to write unit and integration tests: TestCase/BuildDefinition structs, build tags, go-cmp comparison, test coverage matrix |
| [debugging](debugging/SKILL.md) | How to debug scan failures and CI problems: CAPO_DEBUG mode, buildah storage inspection, build tag quirks, CI failure patterns |

## Setup for Claude Code

Skills are symlinked from `.claude/skills/` for automatic discovery:

```
.claude/skills -> ../skills
```

## Setup for Other Agents

Create a symlink from your agent's skill directory to `skills/`:

```bash
ln -s ../skills .agents/skills
```
