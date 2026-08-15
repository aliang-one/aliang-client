# Settings Page Hook Invariants Contract

> **CRITICAL**: These invariants MUST be preserved during styling alignment to prevent breaking JavaScript behavior.

## Page Container Elements

### Dashboard Page
- **Element ID**: `dashboard-page`
- **Required Classes**: Must support `active` and `hidden` toggling
- **Role**: Main dashboard container that gets hidden when settings is shown

### Settings Page
- **Element ID**: `settings-page`
- **Required Classes**: Must support `active` and `hidden` toggling
- **Role**: Settings container that replaces dashboard view
- **Required Structure**: Must contain header with back button + content area with tabs

## Navigation Trigger Elements

### Settings Entry Points
| Element ID | Event | Expected Behavior |
|------------|-------|-------------------|
| `goToSettingsBtn` | click | Calls `showSettingsPage()` - must transition to settings page |
| `headerSettingsBtn` | click | Calls `showSettingsPage()` - must transition to settings page |

### Dashboard Return Point
| Element ID | Event | Expected Behavior |
|------------|-------|-------------------|
| `backToDashboard` | click | Calls `showDashboardPage()` - must return to dashboard |

## Settings Tab System

### Tab Buttons
- **Class Selector**: `.settings-tab`
- **Required Attribute**: `data-tab` with values: `rules`, `userinfo`, `logs`, `system`
- **Active State**: Must toggle `.active` class on click
- **Styling Requirements**: Active tab has `border-primary` and `text-primary` classes

### Tab Content Panels
- **Class Selector**: `.settings-content`
- **Required Attribute**: `data-content` with values matching `data-tab`
- **Visibility Contract**: 
  - Active: Has `.active` class, no `.hidden` class
  - Inactive: Has `.hidden` class, no `.active` class
- **Mapping**:
  - `data-tab="rules"` ↔ `data-content="rules"`
  - `data-tab="userinfo"` ↔ `data-content="userinfo"`
  - `data-tab="logs"` ↔ `data-content="logs"`
  - `data-tab="system"` ↔ `data-content="system"`

## Critical Action Element IDs

### Rules Settings Tab
- `rulesEnableBtn` - Enable rules engine
- `rulesDisableBtn` - Disable rules engine
- `rulesReloadBtn` - Reload rules
- `rulesClearCacheBtn` - Clear cache
- `geoipEnabledSwitch` - GeoIP routing toggle
- `nonelaneEnabledSwitch` - None lane toggle
- `rulesLookupDomain` - Domain lookup input
- `rulesLookupBtn` - Domain query button
- `rulesLookupResult` - Query result display

### User Info Tab
- `authUserInfoContainer` - User info display container
- `userBalance` - Balance display element
- `authTokenInput` - Token input field
- `authActivateBtn` - Activate token button
- `authLogoutBtn` - Logout button

### Logs Tab
- `logLevelSelect` - Log level dropdown
- `logSourceSelect` - Log source dropdown
- `logsRefreshBtn` - Refresh logs button
- `logsClearBtn` - Clear logs button
- `wsConnectBtn` - WebSocket connect button
- `wsDisconnectBtn` - WebSocket disconnect button
- `logsOutput` - Log output container
- `wsConnectionStatus` - Connection status display

### System Settings Tab

#### Run Mode Section
- `runModeTun` - TUN mode radio button
- `runModeHttp` - HTTP mode radio button
- `runStartBtn` - Start service button
- `runStopBtn` - Stop service button
- `runModeBtn` - Switch mode button
- `runCurrentMode` - Current mode display
- `runServiceStatus` - Service status display
- `runAvailableModes` - Available modes display
- `runStatusInfo` - Status info display

#### Certificate Section
- `cert-type-select` - Certificate type dropdown
- `btn-check-cert` - Check certificate button
- `btn-export-cert` - Export certificate button
- `btn-download-cert` - Download certificate button
- `btn-install-cert` - Install certificate button
- `btn-remove-cert` - Remove certificate button
- `cert-status-container` - Certificate status container
- `cert-status-content` - Certificate status content

## Dashboard Shortcut Contract

### Certificate Management Shortcuts
| Trigger Element | Expected Flow |
|----------------|---------------|
| `dashBtnCheckCert` | 1. Call `showSettingsPage()`<br>2. Click `.settings-tab[data-tab="system"]` |
| `dashBtnInstallCert` | 1. Call `showSettingsPage()`<br>2. Click `.settings-tab[data-tab="system"]`<br>3. Click `btn-install-cert` |

## CSS Class Toggling Contract

### Page Visibility
```javascript
// Dashboard to Settings
dashboardPage.classList.remove('active');
dashboardPage.classList.add('hidden');
settingsPage.classList.remove('hidden');
settingsPage.classList.add('active');

// Settings to Dashboard
settingsPage.classList.remove('active');
settingsPage.classList.add('hidden');
dashboardPage.classList.remove('hidden');
dashboardPage.classList.add('active');
```

### Tab Active State
```javascript
// On tab click
settingsTabs.forEach(btn => btn.classList.remove('active'));
tab.classList.add('active');

// On content switch
content.classList.add('active');
content.classList.remove('hidden');
// OR
content.classList.remove('active');
content.classList.add('hidden');
```

## Global State Dependency

### appState.currentPage
- Must track current page: `'dashboard'` or `'settings'`
- Set by: `showSettingsPage()` and `showDashboardPage()`
- Used by: Various polling and refresh logic

## Forbidden Changes

### DO NOT:
1. Change any element IDs listed above
2. Modify `data-tab` or `data-content` attribute values
3. Change the class toggling logic in `app.js` (lines 3593-3703)
4. Remove the `.settings-tab` or `.settings-content` class selectors
5. Alter the event listener attachment pattern
6. Change the `.active` and `.hidden` class usage pattern
7. Modify the `data-tab` → `data-content` mapping logic

## Safe Styling Areas

### CAN Modify:
1. CSS properties of `.settings-tab` (colors, borders, padding, etc.)
2. CSS properties of `.settings-content` containers (spacing, borders, shadows)
3. Visual styling of child elements within content panels
4. Add new wrapper elements that don't interfere with existing IDs/classes
5. Responsive breakpoints and mobile layouts
6. Dark mode color variations
7. Typography, spacing, and visual hierarchy

## Testing Validation Points

After any styling changes, verify:
- [ ] All 4 tabs switch correctly
- [ ] Back button returns to dashboard
- [ ] Dashboard shortcuts navigate to system tab
- [ ] All form inputs are functional
- [ ] All buttons are clickable
- [ ] No console errors during page/tab transitions
- [ ] Dark mode toggle preserves functionality
- [ ] Mobile responsive layout works
- [ ] No visual overlap of active/inactive content

## Version
- **Date**: 2026-03-18
- **Related Files**: `app/website/index.html`, `app/website/assets/app.js`, `app/website/assets/styles.css`
- **Reference Design**: `app/website/reference/settings.html`
