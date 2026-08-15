# ALiang Gateway - Getting Started Guide

## Table of Contents

0. [Prerequisites](#0-prerequisites)
1. [Quick Start](#quick-start-four-steps)
2. [First Launch - Installation & Login](#1-first-launch--installation--login)
3. [Trust Certificate (Unlogged State)](#2-trust-certificate-unlogged-state)
4. [Login Flow](#3-login-flow)
5. [Quick Setup](#4-quick-setup)
6. [Run Mode Selection](#5-run-mode-selection)
7. [Customer Proxy Configuration](#6-customer-proxy-configuration)
8. [AI Rules Configuration](#7-ai-rules-configuration)
9. [Proxy Rules Configuration](#8-proxy-rules-configuration)
10. [FAQ](#9-faq)

---

## 0. Prerequisites

### What You Need

- ✅ ALiang Gateway account
- ✅ Valid API key (pre-loaded in your account)
- ❌ Upstream proxy (optional, not needed for most users)

> 💡 **Tip**: API keys are pre-loaded in your account. You can select them directly in Quick Setup after logging in. No need to apply from OpenAI/Anthropic yourself.

---

## Quick Start (Four Steps)

Complete these four steps to start accelerating your AI traffic:

```
1. Install Certificate → 2. Log In → 3. Quick Setup → 4. Start Proxy
```

### Step 1: Install Certificate

Find the **Certificate CA** area, click **Install to System**, and confirm authorization. Status will show **"Trusted"** when complete.

### Step 2: Log In

Enter your email and password on the dashboard. After successful login, all features will be unlocked.

### Step 3: Quick Setup

If you use clients that require manual configuration, such as **Codex, Claude Code, or OpenCode**, click **Quick Setup** on the dashboard, select the target software, choose an API key, then click **Apply Files** to write the configuration to the specified path.

> 💡 **Tip**: If you want to use the official native AI interface in **VS Code** or **Cursor**, use **Deep Mode** instead of relying on manual setup in Regular Mode.

### Step 4: Start Proxy

Return to the dashboard, click **Start Proxy**.

### How to Verify It's Working?

- Dashboard shows "Proxy Running" ✓
- Open Cursor/VS Code or other AI software and try using AI features
- If AI responds normally, the configuration is successful

> 💡 **Tip**: After switching run mode, the proxy will remain stopped. You need to manually click "Start Proxy".

---

## 1. First Launch - Installation & Login

### Opening the App for the First Time

After downloading and installing ALiang Gateway, the app opens in an **unlogged state**.

**Available in unlogged state:**
- Certificate management (install, export, regenerate)
- Language and appearance settings
- Help documentation

**Unavailable in unlogged state:**
- Start/stop proxy
- Quick Setup
- Customer configuration changes
- User Center (balance, top-up, package redemption, etc.)

---

## 2. Trust Certificate (Unlogged State)

> ⚠️ **Important**: Certificate status must be **"Trusted"** for AI traffic acceleration to work properly.

### Why Install the Certificate?

ALiang Gateway needs to install a local certificate to identify and process AI traffic (such as OpenAI, Claude, Cursor, VS Code, etc.) to enable intelligent routing and acceleration.

**Certificate Safety:**
- Certificate is only used to identify and accelerate AI traffic
- Does not record or transmit any of your data
- Certificate is stored on your device and cannot be accessed by third parties

### Installation Steps

1. Find the **Certificate CA** area on the dashboard or sidebar
2. Click **Install to System**
3. Confirm the system authorization prompt
4. After installation, the certificate status shows **"Trusted"**

### Certificate Status Explained

| Status | Meaning |
|--------|---------|
| Generated | Certificate created, but not installed to system |
| Installed | Certificate installed to system |
| Trusted | Certificate installed and system-trusted, AI traffic acceleration works |

### Export Certificate (Optional)

To use on other devices, click **Export** to download the PEM format certificate file.

---

## 3. Login Flow

> ⚠️ **Important**: User must be **logged in** to use proxy start/stop, configuration changes, and other features.

### How to Login

1. Find the **Account Access** area on the dashboard
2. Enter email and password
3. Click **Login**

### After Successful Login

- Dialog closes automatically
- Account status shows "Authenticated session active"
- Unlocked features:
  - Proxy start/stop
  - Quick Setup
  - Customer configuration changes
  - Quick Chat
  - Account balance and top-up

### Session Persistence

After login, session information is saved locally. Opening the app again will automatically restore the logged-in state.

---

## 4. Quick Setup

Quick Setup helps you generate ready-to-use configuration files for popular AI software such as **Codex, OpenCode, and Claude Code** with one click.

> 💡 **Scope**: Quick Setup is mainly for clients that let you manually configure API endpoints, proxies, or keys. If you want to use the official native AI interface in **VS Code** or **Cursor**, use **Deep Mode**.

### Steps

1. Click **Quick Setup** on the dashboard
2. Select the client from preset templates (such as Codex, OpenCode, or Claude Code)
3. Choose a configured API key
4. The system automatically generates configuration files
5. Click **Apply Files** to write configurations to the specified path

### Configuration File Location

**Important: Use the full absolute path, do NOT use `~` to represent the home directory.**

For example:
- ✅ Correct: `/Users/yourusername/.config/xxx/config.json`
- ❌ Wrong: `~/.config/xxx/config.json`

### Adding Custom Software

If your software is not in the presets:

1. Click **+ Add Software**
2. Fill in software name and description
3. Configure the configuration file content
4. Save

---

## 5. Run Mode Selection

### Mode Comparison

| Scenario | Recommended Mode |
|----------|-----------------|
| Use the official native AI interface in Cursor or VS Code directly | Deep Mode |
| Connect Codex, Claude Code, OpenCode, and other CLI/manual-config clients | Regular Mode or Deep Mode |
| Route AI traffic for all software automatically without per-app setup | Deep Mode |
| Command-line tools (curl, wget, etc.) | Regular Mode |

### Deep Mode

Deep Mode works through a **TUN virtual network interface** and accelerates AI traffic for **all software**, including:

- **Cursor** - AI code editor
- **VS Code** - with AI extensions
- **Claude Code** - Anthropic's official CLI tool
- **OpenCode** - open source AI coding tool
- **Any application using AI services**

Deep Mode allows these applications to directly use our AI services with faster response speeds and stable connections.

**Important notes:**
- Supports the **official native AI interface in VS Code**
- Supports the **official native AI interface in Cursor**
- Also works for CLI tools such as **Codex, Claude Code, and OpenCode**
- In most cases, no per-app proxy configuration is needed

**Advantages:**
- No need to configure proxy for each application
- Supports AI traffic acceleration for all software
- Global effect, simple to use

**Notes:**
- Windows users: necessary drivers auto-install on first switch
- May conflict with VPNs that also use deep mode type connections
- Before switching, recommended to set other VPNs to non-deep mode

### Regular Mode

Regular Mode works through a local **HTTP proxy** and is suitable for:

- **Command-line tools** - curl, wget, git, etc.
- **Clients that require manual configuration** - such as Codex, Claude Code, and OpenCode
- **Scenarios requiring fine-grained proxy control**

**Important notes:**
- In Regular Mode, users must **manually configure the client** through proxy settings or config files
- Regular Mode does **not** support the **official native AI interface in VS Code**
- Regular Mode does **not** support the **official native AI interface in Cursor**
- If you want to use AI directly inside the native VS Code / Cursor interface, switch to **Deep Mode**

**Configuration:**
In each app's proxy settings or configuration file:
- Proxy type: HTTP Proxy or SOCKS5
- Address: `127.0.0.1`
- Port: `56432`

---

## 6. Customer Proxy Configuration (Optional)

> 💡 **Tip**: Most users don't need to configure this. If you have your own proxy server to forward traffic to, refer to this section.

In **Customer Configuration**, you can set your own VPN or proxy as the upstream proxy.

### Function

Use ALiang as an intermediate proxy, forwarding traffic to your own VPN (SOCKS or HTTP).

### Steps

1. Go to **Settings** → **Customer Configuration**
2. Find the **Customer Proxy** area
3. Enable customer proxy
4. Select proxy type:
   - **SOCKS5** - if your VPN provides SOCKS5 protocol
   - **HTTP** - if your VPN provides HTTP proxy
5. Fill in server address and port
   - Format: `IP:Port` (e.g., `127.0.0.1:1080`)
   - Port must be between 1-65535
6. Click **Save Configuration**

### Use Case

```
Your Device → ALiang Gateway (56432) → Your VPN (SOCKS/HTTP) → Target Server
```

Suitable for:
- Already have VPN lines, want to accelerate AI traffic through ALiang
- Need intelligent routing for specific domains

---

## 7. AI Rules Configuration

AI Rules let you customize which AI service traffic goes through proxy acceleration.

### Supported Providers

- **OpenAI** - ChatGPT, API calls
- **Anthropic** - Claude
- **VS Code** - VS Code AI extensions
- Other AI services with custom endpoints

### Steps

1. Go to **Settings** → **Customer Configuration**
2. Find the **AI Rules** area
3. Select AI providers to enable (multiple allowed)
4. Configure **Included Domains** for each provider

### Domain Configuration Examples

Supports full URLs or plain domain names:

```
# Can write like this:
https://api.openai.com/v1
https://chatgpt.com
anthropic.com
claude.ai

# System automatically extracts domain portion
```

### Privacy Notice

**Putting third-party APIs into AI Rules does NOT leak your privacy.**

Why:
- ALiang only determines if traffic is AI traffic based on the domain
- Does not inspect or log your request content
- ALiang acts as a transparent proxy, only handling routing and acceleration

---

## 8. Proxy Rules Configuration

Proxy Rules let you customize traffic routing for specific domains.

### Function

You can specify that certain domains:
- **Use proxy** - Traffic goes through your configured proxy server
- **Direct connect** - Traffic connects directly without proxy

### Steps

1. Go to **Settings** → **Customer Configuration**
2. Find the **Proxy Rules** area
3. One rule per line, format:

```
domain,example.com,proxy
```

4. Rule explanation:
   - Part 1: Fixed as `domain`
   - Part 2: Domain to match
   - Part 3: `proxy` (use proxy) or `direct` (direct connect)

### Examples

```
# These domains use proxy
domain,openai.com,proxy
domain,anthropic.com,proxy
domain,github.com,proxy

# These domains connect directly
domain,google.com,direct
```

---

## 9. FAQ

### Q: Nothing happens when I click Start Proxy?

**A:**
1. Confirm certificate status is "Trusted"
2. Confirm you are logged in
3. Check runtime logs for error messages

### Q: Deep Mode conflicts with other VPN. What to do?

**A:** Recommended to switch other VPNs to non-deep mode (global mode). If both must be used simultaneously, AI traffic may be unstable.

### Q: Why can't the official native AI interface in VS Code / Cursor work directly in Regular Mode?

**A:** Because Regular Mode is a local `HTTP` proxy mode and is intended for clients that allow manual proxy, API endpoint, or config file setup, such as Codex, Claude Code, and OpenCode. For the official native AI interface in VS Code or Cursor, use **Deep Mode**.

### Q: Certificate shows "Not Trusted" but it's installed?

**A:**
1. Try restarting the app
2. If still not working, click "Reinstall" certificate
3. macOS users may need to manually trust the certificate in System Settings → Privacy & Security

### Q: Configuration file write fails?

**A:**
1. Check if target path exists
2. Ensure using **full absolute path**, do NOT use `~`
3. Ensure write permission for that path
4. Check available disk space

### Q: AI traffic not accelerated?

**A:**
1. Confirm the domain is added in AI Rules
2. Confirm proxy is started
3. Check upstream proxy (Customer Proxy) configuration is correct
4. Check runtime logs to troubleshoot

### Q: Will configuration be lost after logout?

**A:** No. Customer configuration is saved on the server side. It automatically restores after re-login.

### Q: What happens if my account balance is insufficient?

**A:** If your account balance is insufficient, the proxy service will not be able to start. You can check your balance and top up in the User Center.

---

## Quick Reference

| Action | Location |
|--------|----------|
| Login/Logout | Dashboard → Account Access |
| Install Certificate | Dashboard → Certificate CA → Install |
| Switch Mode | Settings → System Settings → Run Mode |
| Configure Customer Proxy | Settings → Customer Configuration → Customer Proxy |
| Configure AI Rules | Settings → Customer Configuration → AI Rules |
| Configure Proxy Rules | Settings → Customer Configuration → Proxy Rules |
| Quick Setup | Dashboard → Quick Setup |
| View Logs | Settings → Log Monitoring |

---

*Document version: 2026-04-14*
