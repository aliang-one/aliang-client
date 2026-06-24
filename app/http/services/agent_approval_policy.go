package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"aliang.one/nursorgate/common/logger"
)

const approvalPolicyFilename = "approval_policy.json"

// approvalPolicyCachePath resolves the on-disk policy cache path alongside the
// other agent state files (agent_state.json / device_identity.json).
func approvalPolicyCachePath() (string, error) {
	statePath, err := agentStatePath()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(statePath), approvalPolicyFilename), nil
}

// normalizePolicyPath canonicalizes a project path for use as a cache/sync key:
// trimmed, forward-slashed, no trailing slash. Empty stays empty — the empty key
// means "no project" and always resolves to the built-in balanced policy.
func normalizePolicyPath(projectPath string) string {
	p := strings.TrimSpace(projectPath)
	if p == "" {
		return ""
	}
	p = strings.ReplaceAll(p, "\\", "/")
	p = strings.TrimRight(p, "/")
	return p
}

// approvalPolicyCacheFile is the persisted shape: a map from normalized project
// path to its last-synced policy. It survives process restarts so a transient
// server outage does not drop a project back to the built-in default.
type approvalPolicyCacheFile struct {
	ByPath map[string]ApprovalPolicy `json:"by_path"`
}

// loadPolicyCache reads, unmarshals, and compiles every cached per-project
// policy. It returns ok=false on absent, corrupt, or regex-invalid input so the
// caller falls back to the built-in default per path rather than mis-evaluating.
func loadPolicyCache() (map[string]ApprovalPolicy, bool) {
	path, err := approvalPolicyCachePath()
	if err != nil {
		return nil, false
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	var file approvalPolicyCacheFile
	if err := json.Unmarshal(raw, &file); err != nil {
		return nil, false
	}
	if len(file.ByPath) == 0 {
		return nil, false
	}
	out := make(map[string]ApprovalPolicy, len(file.ByPath))
	for k, p := range file.ByPath {
		if err := compileApprovalRules(&p); err != nil {
			return nil, false
		}
		out[normalizePolicyPath(k)] = p
	}
	return out, true
}

// savePolicyCache writes the whole by-path map atomically (temp file + rename)
// so a torn write can never leave a half-written policy for the next run to
// mis-evaluate.
func savePolicyCache(byPath map[string]ApprovalPolicy) error {
	path, err := approvalPolicyCachePath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	file := approvalPolicyCacheFile{ByPath: make(map[string]ApprovalPolicy, len(byPath))}
	for k, p := range byPath {
		file.ByPath[normalizePolicyPath(k)] = p
	}
	raw, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// ---- AgentService integration (per-project) ----

// effectiveApprovalPolicyForPath returns the policy governing one project path,
// in priority order: in-memory (last synced for this path) > on-disk cache >
// built-in balanced default. An empty or unknown path resolves to the built-in
// balanced policy (fail-safe): losing contact with the server, or running in a
// not-yet-reported directory, never degrades to "approve everything". Safe from
// any goroutine; takes s.mu. A cache hit is memoized into s.policyByPath.
func (s *AgentService) effectiveApprovalPolicyForPath(projectPath string) ApprovalPolicy {
	key := normalizePolicyPath(projectPath)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.policyByPath != nil {
		if p, ok := s.policyByPath[key]; ok {
			return *p
		}
	}
	if cached, ok := loadPolicyCache(); ok {
		if p, ok := cached[key]; ok {
			if s.policyByPath == nil {
				s.policyByPath = make(map[string]*ApprovalPolicy)
			}
			stored := p
			s.policyByPath[key] = &stored
			return p
		}
	}
	return builtinBalancedPolicy()
}

// setEffectivePolicyForPathLocked stores a freshly synced policy for one path
// and is the single write point for s.policyByPath[path]. Caller must hold s.mu.
func (s *AgentService) setEffectivePolicyForPathLocked(projectPath string, p ApprovalPolicy) {
	key := normalizePolicyPath(projectPath)
	if s.policyByPath == nil {
		s.policyByPath = make(map[string]*ApprovalPolicy)
	}
	stored := p
	s.policyByPath[key] = &stored
}

// evaluateApprovalDecision evaluates the effective policy for the given project
// path for one tool call. Convenience used by the approval hooks.
func (s *AgentService) evaluateApprovalDecision(toolName string, toolInput json.RawMessage, projectPath string) (policyDecision, string) {
	return evaluateApprovalPolicy(s.effectiveApprovalPolicyForPath(projectPath), toolName, toolInput)
}

// ---- Server sync (pull safety net; push via project.settings.updated is primary) ----

const agentPolicySyncThrottle = 60 * time.Second

// ensurePolicyBeforeRun is a best-effort pre-run sync of one project's approval
// policy. It pulls the server's current hash for that project_path and refetches
// the full policy only when it changed. It is throttled per path
// (project.settings.updated is the primary update path) and NEVER blocks beyond
// a short timeout or errors the caller — on any failure it keeps using the
// cached/built-in policy.
func (s *AgentService) ensurePolicyBeforeRun(ctx context.Context, projectPath string) {
	key := normalizePolicyPath(projectPath)
	s.mu.Lock()
	token := strings.TrimSpace(s.state.DeviceToken)
	var last time.Time
	if s.policyLastCheckAtPath != nil {
		last = s.policyLastCheckAtPath[key]
	}
	s.mu.Unlock()
	if token == "" {
		return
	}
	if !last.IsZero() && time.Since(last) < agentPolicySyncThrottle {
		return
	}
	hashCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	remoteHash, _, err := s.fetchPolicyHash(hashCtx, key)
	if err != nil {
		logger.Warn(fmt.Sprintf("approval-policy: hash fetch failed for path=%q, using cached/builtin: %v", key, err))
		s.markPolicyChecked(key)
		return
	}
	current := s.effectiveApprovalPolicyForPath(projectPath)
	if remoteHash != "" && remoteHash == current.Hash {
		s.markPolicyChecked(key)
		return
	}
	policy, err := s.fetchPolicy(hashCtx, key)
	if err != nil {
		logger.Warn(fmt.Sprintf("approval-policy: full fetch failed for path=%q, using cached/builtin: %v", key, err))
		s.markPolicyChecked(key)
		return
	}
	s.mu.Lock()
	s.setEffectivePolicyForPathLocked(projectPath, policy)
	if s.policyLastCheckAtPath == nil {
		s.policyLastCheckAtPath = make(map[string]time.Time)
	}
	s.policyLastCheckAtPath[key] = time.Now()
	s.mu.Unlock()
	s.persistPolicyCacheForPath(projectPath, policy)
	logger.Info(fmt.Sprintf("approval-policy: synced path=%q scheme=%s version=%d hash=%s", key, policy.Scheme, policy.Version, policy.Hash))
}

// persistPolicyCacheForPath merges one path's policy into the on-disk cache
// without clobbering the other paths, then saves atomically.
func (s *AgentService) persistPolicyCacheForPath(projectPath string, p ApprovalPolicy) {
	key := normalizePolicyPath(projectPath)
	cached, _ := loadPolicyCache()
	if cached == nil {
		cached = make(map[string]ApprovalPolicy)
	}
	cached[key] = p
	if err := savePolicyCache(cached); err != nil {
		logger.Warn(fmt.Sprintf("approval-policy: cache save failed for path=%q: %v", key, err))
	}
}

// markPolicyChecked records a sync attempt for one path so the throttle window
// applies even when the fetch failed (avoid hammering a broken server every turn).
func (s *AgentService) markPolicyChecked(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.policyLastCheckAtPath == nil {
		s.policyLastCheckAtPath = make(map[string]time.Time)
	}
	s.policyLastCheckAtPath[key] = time.Now()
}

// resetPolicySyncThrottleLocked clears one path's throttle timestamp so the next
// ensurePolicyBeforeRun pulls immediately (used when a push signals a change).
// Caller must hold s.mu.
func (s *AgentService) resetPolicySyncThrottleLocked(key string) {
	if s.policyLastCheckAtPath == nil {
		s.policyLastCheckAtPath = make(map[string]time.Time)
	}
	delete(s.policyLastCheckAtPath, key)
}

// doAgentServerGET issues an authenticated GET to an agent-server endpoint using
// the device token, honoring the request context (callers pass a short timeout).
func (s *AgentService) doAgentServerGET(ctx context.Context, endpoint string) ([]byte, error) {
	deviceToken := strings.TrimSpace(s.state.DeviceToken)
	if deviceToken == "" {
		return nil, errors.New("device token is empty")
	}
	authHeader := deviceToken
	if !strings.HasPrefix(strings.ToLower(authHeader), "bearer ") {
		authHeader = "Bearer " + authHeader
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", authHeader)
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("agent server %s: status %d", endpoint, resp.StatusCode)
	}
	return raw, nil
}

// appendProjectPathQuery adds ?project_path=<normalized> when the path is
// non-empty. The server resolves (device, path) -> project; an absent/empty
// param means "no project", for which the server returns the built-in balanced.
func appendProjectPathQuery(endpoint, projectPath string) string {
	key := normalizePolicyPath(projectPath)
	if key == "" {
		return endpoint
	}
	q := url.Values{}
	q.Set("project_path", key)
	sep := "?"
	if strings.Contains(endpoint, "?") {
		sep = "&"
	}
	return endpoint + sep + q.Encode()
}

func (s *AgentService) fetchPolicyHash(ctx context.Context, projectPath string) (string, int, error) {
	endpoint := appendProjectPathQuery(strings.TrimRight(currentAgentServerURL(), "/")+"/api/agent/approval-policy/hash", projectPath)
	raw, err := s.doAgentServerGET(ctx, endpoint)
	if err != nil {
		return "", 0, err
	}
	var resp struct {
		Version int    `json:"version"`
		Hash    string `json:"hash"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return "", 0, err
	}
	return resp.Hash, resp.Version, nil
}

func (s *AgentService) fetchPolicy(ctx context.Context, projectPath string) (ApprovalPolicy, error) {
	endpoint := appendProjectPathQuery(strings.TrimRight(currentAgentServerURL(), "/")+"/api/agent/approval-policy", projectPath)
	raw, err := s.doAgentServerGET(ctx, endpoint)
	if err != nil {
		return ApprovalPolicy{}, err
	}
	var p ApprovalPolicy
	if err := json.Unmarshal(raw, &p); err != nil {
		return ApprovalPolicy{}, err
	}
	if err := compileApprovalRules(&p); err != nil {
		return ApprovalPolicy{}, err
	}
	return p, nil
}

// remoteApprovalPolicyHash extracts the approval_policy.hash the server pushed,
// checking both the top-level message and (optionally) a nested object. Works
// for project.settings.updated (device=nil) and the legacy device payload.
// Returns "" when absent (no policy push).
func remoteApprovalPolicyHash(msg, device map[string]interface{}) string {
	for _, src := range []map[string]interface{}{msg, device} {
		if src == nil {
			continue
		}
		if ap, ok := src["approval_policy"].(map[string]interface{}); ok {
			if h := strings.TrimSpace(remoteString(ap, "hash")); h != "" {
				return h
			}
		}
	}
	return ""
}

// Approval policy template evaluation.
//
// The agent owns a per-project approval policy (fetched from the cloud, cached
// locally keyed by project path). Before escalating a tool call to the cloud for
// human approval, the hook evaluates the current session's project policy
// locally: read-only / safe operations are auto-approved without a round-trip;
// only file mutation, dangerous commands, and unmatched (fail-safe) operations
// escalate. A session whose cwd is not a known project uses the built-in
// balanced policy. See
// docs/superpowers/specs/2026-06-22-project-scoped-approval-policy-design.md.

// policyDecision is the outcome of evaluating a policy rule (or the default).
type policyDecision string

const (
	decisionAutoApprove     policyDecision = "auto_approve"
	decisionRequireApproval policyDecision = "require_approval"
	decisionAutoDeny        policyDecision = "auto_deny"
)

// approvalRuleMatch constrains which tool invocations a rule applies to.
// Tool is an OR-allowlist of tool names. CommandRegex (optional) is matched
// against the Bash command string; when empty the rule matches on tool alone.
type approvalRuleMatch struct {
	Tool         []string `json:"tool,omitempty"`
	CommandRegex string   `json:"command_regex,omitempty"`
}

// approvalRule is one ordered rule. cmdRe is the compiled CommandRegex
// (populated by compileApprovalRules, not serialized).
type approvalRule struct {
	ID       string            `json:"id"`
	Match    approvalRuleMatch `json:"match"`
	Decision policyDecision    `json:"decision"`
	Reason   string            `json:"reason,omitempty"`
	cmdRe    *regexp.Regexp    `json:"-"`
}

// ApprovalPolicy is the resolved policy the agent evaluates. It is the unit
// cached on disk (keyed by project path) and hashed by the server.
type ApprovalPolicy struct {
	Version         int            `json:"version"`
	Hash            string         `json:"hash,omitempty"`
	Scheme          string         `json:"scheme"` // balanced | allow_all | custom
	DeviceID        string         `json:"device_id,omitempty"`
	Rules           []approvalRule `json:"rules"`
	DefaultDecision policyDecision `json:"default_decision"`
}

// compileApprovalRules compiles every rule's CommandRegex into cmdRe. A rule
// with no regex is left with cmdRe==nil (matches on tool alone). Returns the
// first compile error so a malformed policy can be rejected upstream and the
// caller can fall back to the built-in default.
func compileApprovalRules(p *ApprovalPolicy) error {
	for i := range p.Rules {
		pattern := strings.TrimSpace(p.Rules[i].Match.CommandRegex)
		if pattern == "" {
			p.Rules[i].cmdRe = nil
			continue
		}
		re, err := regexp.Compile(pattern)
		if err != nil {
			return err
		}
		p.Rules[i].cmdRe = re
	}
	return nil
}

// evaluateApprovalPolicy returns the decision + matched rule id for a tool call.
// Rules are evaluated in order; the first match wins. If none match, the
// policy's DefaultDecision applies (fail-safe for balanced, allow-all for
// allow_all). A rule with a CommandRegex requires a non-empty, matching command
// — an unparsable Bash command therefore falls through to the default rather
// than silently auto-approving.
func evaluateApprovalPolicy(p ApprovalPolicy, toolName string, toolInput json.RawMessage) (policyDecision, string) {
	toolName = strings.TrimSpace(toolName)
	for _, r := range p.Rules {
		if ruleMatches(r, toolName, toolInput) {
			return r.Decision, r.ID
		}
	}
	return p.DefaultDecision, ""
}

func ruleMatches(r approvalRule, toolName string, toolInput json.RawMessage) bool {
	if len(r.Match.Tool) > 0 && !containsString(r.Match.Tool, toolName) {
		return false
	}
	if r.cmdRe == nil {
		return true
	}
	cmd := commandFromToolInput(toolInput)
	return cmd != "" && r.cmdRe.MatchString(cmd)
}

// commandFromToolInput extracts the Bash command from a tool_use input payload
// ({"command":"..."} or {"cmd":"..."}). Empty/invalid input yields "".
func commandFromToolInput(toolInput json.RawMessage) string {
	if len(toolInput) == 0 {
		return ""
	}
	var m map[string]interface{}
	if err := json.Unmarshal(toolInput, &m); err != nil {
		return ""
	}
	if c := strings.TrimSpace(remoteString(m, "command")); c != "" {
		return c
	}
	return strings.TrimSpace(remoteString(m, "cmd"))
}

func containsString(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// ---- Built-in templates (ultimate fallback; shipped in the binary) ----

// builtinBalancedPolicy is the system default: read-only tools and safe bash
// auto-approve; file mutation, dangerous bash, and anything unmatched escalate
// (fail-safe). Rules are ordered dangerous-first so an explicit danger rule
// always beats an overly-broad safe rule evaluated later (defense-in-depth).
func builtinBalancedPolicy() ApprovalPolicy {
	p := ApprovalPolicy{
		Scheme:          "balanced",
		DefaultDecision: decisionRequireApproval,
		Rules: []approvalRule{
			{
				ID:       "dangerous-bash",
				Decision: decisionRequireApproval,
				Reason:   "危险命令，需审批",
				Match: approvalRuleMatch{
					Tool: []string{"Bash"},
					CommandRegex: `rm\s+(-[a-zA-Z]*r[a-zA-Z]*f|-[a-zA-Z]*f[a-zA-Z]*r)` +
						`|\bsudo\b|\bdd\s|mkfs|>\s*/dev/(sd|nvme|disk)` +
						`|git\s+push|git\s+reset\s+--hard|git\s+clean\s+-fd` +
						`|\bcurl\b.*-X\s*(POST|PUT|DELETE|PATCH)|\bwget\b` +
						`|\bkill\s+-9|\bkillall\b|shutdown|reboot|halt` +
						`|\bchmod\s+-R\b|\bchown\s+-R\b|npm\s+publish`,
				},
			},
			{
				ID:       "file-mutation",
				Decision: decisionRequireApproval,
				Reason:   "文件改写，需审批",
				Match:    approvalRuleMatch{Tool: []string{"Write", "Edit", "MultiEdit", "NotebookEdit"}},
			},
			{
				ID:       "readonly-tools",
				Decision: decisionAutoApprove,
				Reason:   "只读工具，自动放行",
				Match: approvalRuleMatch{Tool: []string{
					"Read", "Grep", "Glob", "LS",
					"TodoWrite", "TaskUpdate", "TaskCreate", "WebSearch",
				}},
			},
			{
				ID:       "safe-bash",
				Decision: decisionAutoApprove,
				Reason:   "只读命令，自动放行",
				Match: approvalRuleMatch{
					Tool: []string{"Bash"},
					CommandRegex: `^(ls|cat|head|tail|wc|pwd|echo|grep|rg|find|file|stat` +
						`|git\s+(status|diff|log|show|branch))\b`,
				},
			},
		},
	}
	_ = compileApprovalRules(&p) // hardcoded patterns; errors impossible by construction
	return p
}

// builtinAllowAllPolicy auto-approves everything. Reserved for explicitly
// trusted projects; never used as a fallback (fallback is balanced/fail-safe).
func builtinAllowAllPolicy() ApprovalPolicy {
	return ApprovalPolicy{
		Scheme:          "allow_all",
		DefaultDecision: decisionAutoApprove,
		Rules:           []approvalRule{},
	}
}
