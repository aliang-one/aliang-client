# Project-Scoped Approval Policy — Implementation Plan

> **STATUS (2026-06-22): ALL THREE PHASES COMPLETE & VALIDATED.**
> - Phase A (agent `alianggate`): per-path policy map + cache + sync + `project.settings.updated`
>   push handler. `go vet`/`go build` clean; approval tests green (incl. by-path isolation,
>   unknown-path→balanced, per-path sync, project push). 1 pre-existing unrelated terminal flake.
> - Phase B (server `AliangPhoneServer`): `project_custom_templates` + projects columns + 4 Store
>   methods (resolve by project/path) + agent `?project_path=` endpoints + project PATCH/custom +
>   `project.settings.updated` push + contract. typecheck (server+web) clean; **259 vitest green**;
>   contract **33 samples** verified (device approval_policy removed; ProjectSettingsUpdatedMessage added).
> - Phase C (mobile `AliangVibeCodingPhone`): `ServerProject.approval_policy` + `updateProject` +
>   `patchProjectCustomPolicy` + `fetchProjectApprovalPolicy`; `approvalScheme` threaded via
>   `serverProjectToClient`; `ApprovalPolicyCard` → project-scoped (gear on Custom row only);
>   `CustomApprovalRulesSheet` (开关微调); removed card from DeviceDetailScreen, added to
>   ProjectDetailScreen. `tsc --noEmit` clean; 344 jest pass (4 pre-existing terminal/vibe failures,
>   approval suites green).
> - Optional cleanup (not blocking): `src/api/devices.ts` still has unused device approval helpers
>   (`updateDeviceApprovalScheme`/`patchDeviceCustomPolicy`/`ServerDevice.approval_policy`) — dead,
>   point at removed server routes; safe to delete later.
> Not committed (per CLAUDE.md / repo norms).

> **For agentic workers:** REQUIRED SUB-SKILL: superpowers:subagent-driven-development
> (recommended) or superpowers:executing-plans. Steps use checkbox (`- [ ]`) syntax.
> Spec: `docs/superpowers/specs/2026-06-22-project-scoped-approval-policy-design.md`.

**Goal:** Move approval-policy ownership device → project across all three repos
(agent `alianggate`, server `AliangPhoneServer`, mobile `AliangVibeCodingPhone`),
add a per-project mobile scheme selector + a gear-opened custom rule editor.

**Architecture:** Project `(device_id, path)` owns its policy; agent evaluates by
`session.projectPath`; unknown path → builtin balanced. Push
`project.settings.updated{path,hash}` + pull `?project_path=` hash probe.

**Tech stack:** Go (agent), TS/Express/WS/zod/better-sqlite3 (server),
React Native/zustand (mobile). TDD per task; typecheck/test green before handoff.

Repos:
- Agent: `/Users/mac/MyProgram/GoProgram/nursor/alianggate` (this repo)
- Server: `/Users/mac/MyProgram/AiProgram/vibe_on_phone/AliangPhoneServer`
- Mobile: `/Users/mac/MyProgram/AiProgram/vibe_on_phone/AliangVibeCodingPhone`

---

## Phase A — Agent (`alianggate`, Go)

### Task A1: per-path policy store + resolution (TDD)

**Files:** `app/http/services/agent_approval_policy.go`, `..._test.go`.

- [ ] RED: test `effectiveApprovalPolicyForPath("/p/a")` returns a previously-set
  policy; empty/unknown path → `builtinBalancedPolicy()`; different paths isolated.
- [ ] GREEN: replace `s.policy *ApprovalPolicy` with `policyByPath map[string]*ApprovalPolicy`;
  add `setEffectivePolicyForPathLocked(path, p)`, `effectiveApprovalPolicyForPath(path)`.
  Keep a thin `effectiveApprovalPolicy()` = for-path("") for transition if needed, then remove.
- [ ] Run `go test ./app/http/services/ -run ApprovalPolicy`.

### Task A2: cache shape → by_path (TDD)

- [ ] RED: test cache round-trip `{by_path:{"/p/a":<policy>}}`; corrupt/absent → false;
  preserves two paths independently.
- [ ] GREEN: change `approvalPolicyCache` on-disk JSON to `{"by_path":{...}}`;
  update `loadPolicyCache`/`savePolicyCache` to return/take a map.
- [ ] Tests green.

### Task A3: evaluate + ensurePolicy take projectPath (TDD)

- [ ] RED: test `evaluateApprovalDecision(tool, input, "/p/a")` resolves via path map;
  unknown path → balanced evaluation (readonly auto-approve). Test
  `ensurePolicyBeforeRun(ctx,"/p/a")` calls `?project_path=/p/a`; per-path throttle.
- [ ] GREEN: `evaluateApprovalDecision(toolName, toolInput, projectPath)`;
  `ensurePolicyBeforeRun(ctx, projectPath)`; `fetchPolicyHash(ctx, projectPath)`;
  `fetchPolicy(ctx, projectPath)` → `?project_path=`. Per-path `policyLastCheckAtPath map[string]time.Time`.
- [ ] Tests green.

### Task A4: wire callers

- [ ] `handleClaudeApprovalHook`: `evaluateApprovalDecision(toolName, toolInput, run.projectPath)`;
  policyVersion via `effectiveApprovalPolicyForPath(run.projectPath).Version`.
- [ ] `runUserMessage`: `svc.ensurePolicyBeforeRun(ctx, session.projectPath)` before `go m.runCLI`.
- [ ] `go vet ./... && go build ./...`.

### Task A5: push handler `project.settings.updated` (TDD)

- [ ] RED: test handler extracts `{path, approval_policy.hash}`; hash differs from
  cached-for-path → resets that path throttle + triggers refetch.
- [ ] GREEN: add `applyRemoteProjectSettings` (mirror `applyRemoteDeviceSettings`);
  register the message in the agent WS dispatch.
- [ ] `go test ./app/http/services/` green.

---

## Phase B — Server (`AliangPhoneServer`, TS)

### Task B1: DB — project table + columns + store methods (TDD)

**Files:** `server/src/database.ts`, `server/test/modules/approval/policy-resolve.test.ts`.

- [ ] RED: extend resolve tests — balanced default, allow_all, custom
  override+version-bump, custom-missing→balanced, **unknown path→balanced**,
  per-project isolation.
- [ ] GREEN: `project_custom_templates` table + index; `projects` += 2 columns;
  `getProjectCustomTemplate` / `upsertProjectCustomTemplate` /
  `resolveProjectApprovalPolicy` / `resolveProjectApprovalPolicyByPath`;
  `rowToProject`/`upsertProject` carry columns; seed unchanged.
- [ ] Mirror defaults-only stubs in `postgresDatabase.ts`.
- [ ] `npm test` green.

### Task B2: types + serializers + schemas

- [ ] `types.ts`: `Project` += scheme/version; `Device` drop active use.
- [ ] `modules/device/serializers.ts`: `publicProject(p, policy?)` += approval_policy;
  `publicDevice` remove approval_policy; fix any `.map(publicProject)`.
- [ ] `schemas.ts`: project-update schema += `approval_policy`; drop device approval_policy.

### Task B3: routes — agent + admin/mobile

- [ ] `modules/agent/routes.ts`: `handleAgentApprovalPolicy` / `…Hash` take
  `?project_path=`; resolve by `(device, path)`; unknown → balanced.
- [ ] `modules/routes/projects.ts` (or wherever project PATCH lives): handle
  `approval_policy.scheme`; `PATCH /api/projects/:id/approval-policy/custom`;
  audit; `publishToAgent('project.settings.updated', {path, approval_policy})`;
  `publishToMobiles('projects.updated')`.
- [ ] Remove device approval handling from `modules/routes/devices.ts`.
- [ ] `npm run typecheck` (server+web) + `npm test`.

### Task B4: contract

- [ ] `docs/agent-cloud-contract/`: add `project.settings.updated` + Project.approval_policy;
  remove Device.approval_policy; README/samples/schema.
- [ ] `npm run contract:agent`.

---

## Phase C — Mobile (`AliangVibeCodingPhone`, RN)

### Task C1: API + types

- [ ] `src/api/projects.ts`: `updateProject` carries `approval_policy`; new
  `patchProjectCustomPolicy(id, overrides)`; `ServerProject` += approval_policy.
- [ ] `src/data/platformModels.ts`: `Project` += `approvalScheme?`.
- [ ] Thread through `platformTransport.ts` (`PlatformProjectSnapshot`, `normalizeServerProject`)
  + `store/internals.ts` (`platformProjectToClient`).

### Task C2: ApprovalPolicyCard → project; remove from device

- [ ] `ApprovalPolicyCard.tsx`: props `projectId`/`path`/`scheme`; calls
  `updateProject({approval_policy:{scheme}})`.
- [ ] `DeviceDetailScreen.tsx`: remove `<ApprovalPolicyCard>`.
- [ ] `ProjectDetailScreen.tsx`: add "APPROVAL POLICY" section rendering the card
  with `scheme={project.approvalScheme ?? 'balanced'}`.

### Task C3: gear → custom rule editor (开关微调)

- [ ] `CustomApprovalRulesSheet.tsx` (BottomSheet): load project custom rules
  (from project payload or `GET /api/projects/:id`); each rule 3-way toggle
  (放行/审批/拒绝); save → `patchProjectCustomPolicy`.
- [ ] `ApprovalPolicyCard`: gear (`IconBadge('settings')`) on the Custom row only;
  opens the sheet.
- [ ] `tsc --noEmit` + `jest`.

---

## Completion definition

- Agent evaluates per-project; unknown path → balanced; push+pull sync by path;
  `go test` green.
- Server stores/resolves per-project; `?project_path=` agent endpoints;
  `project.settings.updated` push; typecheck + vitest + contract green.
- Mobile: scheme selector + gear→custom editor on ProjectDetailScreen; device
  card removed; typecheck clean.
- End-to-end: pick a project's scheme on mobile → server pushes → agent refetches
  by path → only file-mutation/dangerous/fail-safe escalate, per project.
