#include "stdbool.h"

extern void systray_ready();
extern void systray_on_exit();
extern void systray_menu_item_selected(int menu_id);
// 本仓补丁:面板模式托盘点击出口——实现在 Go 侧 systray_nonwindows.go 的
// //export systray_tray_clicked,调用点在 systray_darwin.m 的 trayClicked:。
extern void systray_tray_clicked();
void registerSystray(void);
int nativeLoop(void);

void setIcon(const char* iconBytes, int length, bool template);
void setMenuItemIcon(const char* iconBytes, int length, int menuId, bool template);
void setTitle(char* title);
void setTooltip(char* tooltip);
void add_or_update_menu_item(int menuId, int parentMenuId, char* title, char* tooltip, short disabled, short checked, short isCheckable);
void add_separator(int menuId);
void hide_menu_item(int menuId);
void show_menu_item(int menuId);
void quit();
// 本仓补丁:面板模式开关与托盘图标矩形(实现在 systray_darwin.m,Go 包装在
// systray_darwin.go 的 SetPanelMode / TrayRect)。都会被 goroutine 直接调用,
// 函数体整体包 @autoreleasepool。
void set_panel_mode(bool panel);
void tray_rect(int* x, int* y, int* w, int* h, bool* ok);
