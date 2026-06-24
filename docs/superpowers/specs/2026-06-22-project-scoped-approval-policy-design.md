# Project-Scoped Approval Policy — Design Spec

> Date: 2026-06-22. Supersedes the device-scoped design in
> `2026-06-22-agent-approval-policy-template.md` (same engine/preset/custom model;
> only the **owning entity** moves device → project).
> Status: design LOCKED (3 decisions confirmed); implementation in progress.

## Problem

The approval-policy template (built across Phase A/B/C) is **device-scoped**:
`devices.approval_scheme` + `device_custom_templates`, one policy per device.
The user wants it **project-scoped**: each project owns its own policy, the
mobile selector + editor move from the device screen to the project screen, and
the custom editor gets a dedicated rule editor opened via a gear icon.

## Goals

1. Approval-policy granularity = **project** (uniquely `(device_id, path)`).
   The agent evaluates the **current session's project** policy, using
   `session.projectPath` (already present at approval-hook time, `agent_ai.go`).
2. Mobile: scheme selector + custom rule editor live on the project
   (`ProjectDetailScreen`), not the device.
3. Custom editor: gear icon on the **Custom** row → opens a rule editor
   (开关微调: per-rule 放行/审批/拒绝 on the balanced preset's rules).

## Locked decisions

- **Scope model — 纯项目级.** Unmatched cwd (not-yet-reported dir, temp path) →
  builtin **balanced** (fail-safe). Device-level approval policy is **removed**
  from all code paths; the DB columns/table are left in place (harmless dead
  schema) because this is unreleased.
- **Custom editor depth — 开关微调.** Custom template = copy of the balanced
  preset's rules; the editor exposes each rule's decision as a 3-way toggle
  (auto_approve / require_approval / auto_deny). Match conditions (tool list,
  command_regex) are **not** editable.
- **Gear placement — Custom row only.** balanced/allow_all rows show no gear.

## Non-goals

- Full free-form rule editing (match-condition edit, add/remove rules).
- `matched_rule_id` display on the approval card — still blocked on the
  unresolved `ai.approval.*` ↔ `approval.*` server bridge (separate concern,
  see `ai-approval-reliability-design.md`).
- Admin web console UI (`AliangPhoneServer/web` near-empty; out of scope).

## Architecture

```
Mobile (ProjectDetailScreen)                 Server (AliangPhoneServer)
  scheme picker  ──PATCH /api/projects/:id──▶ projects.approval_scheme
  custom gear ───PATCH .../custom──────────▶ project_custom_templates
                                                  │ resolve + hash
                                                  ▼
                              publishToAgent('project.settings.updated',
                                             {path, approval_policy:{scheme,ver,hash}})
                                                  │
Agent (alianggate)                                ▼
  project.settings.updated handler ──▶ reset per-path throttle
  runUserMessage ──ensurePolicyBeforeRun(path)──▶ GET /api/agent/approval-policy?project_path=
  handleClaudeApprovalHook ──evaluateApprovalDecision(tool, input, path)──▶ local first-match
```

Push (`project.settings.updated`) is primary; pull (`?project_path=` hash probe,
per-path 60s throttle, pre-run) is the safety net — mirrors the device design at
project granularity.

## Data model (server, `AliangPhoneServer`)

### Tables (`server/src/database.ts`)
- `system_preset_templates` — **unchanged** (balanced/allow_all global presets, reused).
- **NEW** `project_custom_templates` (replaces the device-scoped custom table in active use):
  ```sql
  CREATE TABLE IF NOT EXISTS project_custom_templates (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL,
    version INTEGER NOT NULL,
    rules_json TEXT NOT NULL,
    default_decision TEXT NOT NULL,
    hash TEXT NOT NULL,
    created_from_preset TEXT,
    updated_at TEXT NOT NULL
  );
  CREATE INDEX IF NOT EXISTS idx_project_custom_project
    ON project_custom_templates(project_id, version DESC);
  ```
- `projects` += `approval_scheme TEXT NOT NULL DEFAULT 'balanced'`,
  `approval_custom_version INTEGER` (via `ensureColumn`).
- `devices.approval_scheme` / `approval_custom_version` / `device_custom_templates`:
  **dead** — left in schema (cannot drop), no code reads/writes them.

### Store methods (`database.ts`)
- `getProjectCustomTemplate(projectId, version?)`
- `upsertProjectCustomTemplate(projectId, policy)` — version+1, rehash,
  `created_from_preset='balanced'`.
- `resolveProjectApprovalPolicy(projectId): ApprovalPolicy` — read
  `project.approvalScheme`: balanced/allow_all → active preset; custom →
  project custom template (missing → balanced preset); fill project_id/version/hash.
- `resolveProjectApprovalPolicyByPath(deviceId, path)` —
  `getProjectByDeviceAndPath` → resolve; **no project → balanced**.
- `rowToProject` / `upsertProject` carry the 2 new columns.
- `postgresDatabase.ts`: defaults-only stubs (PG backend returns balanced/allow_all
  presets, no custom/preset table) — same pattern as the device version.

### Types (`server/src/types.ts`)
- `Project` += `approvalScheme: 'balanced'|'allow_all'|'custom'`,
  `approvalCustomVersion?: number`.
- `Device`: remove `approvalScheme` / `approvalCustomVersion` from active use.

### Serializers
- `publicProject(project, approvalPolicy?)` += `approval_policy: {scheme,version,hash}`.
- `publicDevice`: **remove** the `approval_policy` field added in Phase B.

### Schemas (`server/src/schemas.ts`)
- Extend the project-update schema with
  `approval_policy: { scheme?, custom_rule_overrides? }` (reuses the existing
  `approvalRuleSchema` / `approvalDecisionSchema`).
- Keep `deviceSettingsSchema.approval_policy` removed (no longer used).

### Routes
- **Agent-facing (device_token)** — device from token via `resolveDeviceForCredential`:
  - `GET /api/agent/approval-policy?project_path=<path>` → resolved policy
    (unknown path → balanced).
  - `GET /api/agent/approval-policy/hash?project_path=<path>` → `{version,hash}`.
- **Admin/mobile-facing (`requireUserId` + accessible)**:
  - `PATCH /api/projects/:projectId` — handle `approval_policy.scheme`:
    write `project.approvalScheme`; on switch to custom with no template →
    `upsertProjectCustomTemplate(balanced copy)`; set version; `scheduleStateSave`;
    `rememberAudit`; `publishToAgent('project.settings.updated', …)`;
    `publishToMobiles('projects.updated')`; return `publicProject(p, resolved)`.
  - `PATCH /api/projects/:projectId/approval-policy/custom` — apply
    `custom_rule_overrides` ({ruleId: decision}) to the custom template copy →
    version+1, rehash, push.

## Agent (`alianggate`, Go)

- `AgentService`: replace single `policy *ApprovalPolicy` with
  `policyByPath map[string]*ApprovalPolicy` (guarded by `s.mu`).
- `effectiveApprovalPolicyForPath(projectPath string) ApprovalPolicy` —
  memory-by-path → cache-by-path → **builtin balanced** (never allow-all as
  fallback). Empty/unknown path → builtin balanced.
- `evaluateApprovalDecision(toolName, toolInput, projectPath)` — resolve by path.
- `ensurePolicyBeforeRun(ctx, projectPath)` — per-path 60s throttle, 3s-capped
  hash probe `?project_path=`, refetch on mismatch, graceful degrade. `runUserMessage`
  calls it with `session.projectPath` before `go m.runCLI`.
- `handleClaudeApprovalHook`: pass `run.projectPath` to `evaluateApprovalDecision`.
- Cache file `approval_policy_cache.json` → `{ "by_path": { "<path>": ApprovalPolicy } }`;
  `loadPolicyCache`/`savePolicyCache` updated; bounded by project count.
- **Push handler** `project.settings.updated`: extract `{path, approval_policy.hash}`;
  if differs from cached-for-path → reset that path's throttle + async refetch.
- `ai.approval.request` still carries `matched_rule_id` / `policy_version` (unchanged).

## Mobile (`AliangVibeCodingPhone`, RN)

- **Remove** `<ApprovalPolicyCard>` from `DeviceDetailScreen`.
- `ApprovalPolicyCard` → repurpose to project: props `projectId`, `path`, `scheme`;
  calls `updateProject({ approval_policy: { scheme } })`.
- Add an "APPROVAL POLICY" section to `ProjectDetailScreen` (currently view-only).
- **Gear on Custom row** → opens `CustomApprovalRulesSheet` (BottomSheet):
  loads the project's custom-template rules; each rule = 3-way toggle
  (放行/审批/拒绝); save → `PATCH /api/projects/:id/approval-policy/custom`.
  Reuses `BottomSheet`, `GlassPanel`, `IconBadge('settings')`.
- Types: `Project` += `approvalScheme?`, `approvalPolicy?{scheme,version,hash}`;
  `ServerProject` += same; `updateProject` carries `approval_policy`; new
  `patchProjectCustomPolicy`. Thread `approvalScheme` through
  `platformModels` / `platformTransport` / `store/internals` for projects.

## Contract (`AliangPhoneServer/docs/agent-cloud-contract/`)

- Add `project.settings.updated` message + `approval_policy` on `Project`.
- Remove `approval_policy` from `Device` (Phase B addition).
- Update `README.md` / `samples.json` / `schema.json`; `npm run contract:agent`.

## Testing

- **Agent**: extend `agent_approval_policy_test.go` — by-path map, unknown/empty
  path → balanced, per-path cache round-trip, per-path sync (hash match/mismatch/
  fail-degrade), push refetch by path.
- **Server**: policy engine tests already cover first-match/dangerous-first; add
  project-resolve tests (balanced / allow_all / custom-override+version /
  custom-missing→balanced / unknown-path→balanced) + route + contract.
- **Mobile**: `tsc --noEmit` + jest; manual scheme switch + custom editor open/save.

## Rollout

Unreleased (prior Phase A/B/C uncommitted). No production migration. Device-level
approval code removed; DB artifacts left harmless. End-to-end: mobile picks a
project's scheme → server pushes `project.settings.updated` → agent refetches by
path → only file-mutation / dangerous-command / fail-safe escalate; read-only
tools auto-approve with zero cloud round-trips, per project.
