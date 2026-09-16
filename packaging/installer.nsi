Unicode True
!include "MUI2.nsh"
!include "FileFunc.nsh"
!include "LogicLib.nsh"
!ifndef VERSION
  !error "VERSION is required"
!endif
Name "API Subagents"
OutFile "..\release\API-Subagents-Setup-${VERSION}-x64.exe"
InstallDir "$LOCALAPPDATA\Programs\api-subagents"
InstallDirRegKey HKCU "Software\Microsoft\Windows\CurrentVersion\Uninstall\a3330349-9337-5aca-8bce-ff36046d9870" "InstallLocation"
RequestExecutionLevel user
SetCompressor /SOLID lzma
Icon "icon.ico"
UninstallIcon "icon.ico"
VIProductVersion "${VERSION}.0"
VIAddVersionKey "ProductName" "API Subagents"
VIAddVersionKey "FileDescription" "API Subagents Installer"
VIAddVersionKey "FileVersion" "${VERSION}"
VIAddVersionKey "ProductVersion" "${VERSION}"
VIAddVersionKey "LegalCopyright" "API Subagents contributors"
Var UpdateMode
!define MUI_FINISHPAGE_RUN "$INSTDIR\API Subagents.exe"
!define MUI_FINISHPAGE_RUN_TEXT "启动 API Subagents"
!insertmacro MUI_PAGE_INSTFILES
!insertmacro MUI_PAGE_FINISH
!insertmacro MUI_UNPAGE_CONFIRM
!insertmacro MUI_UNPAGE_INSTFILES
!insertmacro MUI_LANGUAGE "SimpChinese"

; 读取更新模式；安装路径与旧 Electron 版本一致，用户配置保存在独立目录。
Function .onInit
  ${GetParameters} $0
  ClearErrors
  ${GetOptions} $0 "/UPDATE" $1
  ${IfNot} ${Errors}
    StrCpy $UpdateMode "1"
  ${EndIf}
FunctionEnd

Section "Install"
  SetShellVarContext current
  ; 更新器先退出旧窗口，再由安装器替换文件。
  ${If} $UpdateMode == "1"
    Sleep 1500
  ${EndIf}
  ; 迁移 Electron 时调用其现有卸载器清理浏览器运行文件，明确保留用户数据。
  IfFileExists "$INSTDIR\resources\app.asar" 0 install_files
  IfFileExists "$INSTDIR\Uninstall API Subagents.exe" 0 install_files
  ExecWait '"$INSTDIR\Uninstall API Subagents.exe" /S /KEEP_APP_DATA _?=$INSTDIR' $0
  ${If} $0 != 0
    MessageBox MB_ICONSTOP "旧版本卸载未完成，请关闭 API Subagents 后重试。"
    Abort
  ${EndIf}
install_files:
  SetOutPath "$INSTDIR"
  File "..\build\bin\API Subagents.exe"
  File "..\THIRD-PARTY-NOTICES.txt"
  WriteUninstaller "$INSTDIR\Uninstall API Subagents.exe"
  CreateShortcut "$DESKTOP\API Subagents.lnk" "$INSTDIR\API Subagents.exe"
  CreateShortcut "$SMPROGRAMS\API Subagents.lnk" "$INSTDIR\API Subagents.exe"
  WriteRegStr HKCU "Software\Microsoft\Windows\CurrentVersion\Uninstall\a3330349-9337-5aca-8bce-ff36046d9870" "DisplayName" "API Subagents"
  WriteRegStr HKCU "Software\Microsoft\Windows\CurrentVersion\Uninstall\a3330349-9337-5aca-8bce-ff36046d9870" "DisplayVersion" "${VERSION}"
  WriteRegStr HKCU "Software\Microsoft\Windows\CurrentVersion\Uninstall\a3330349-9337-5aca-8bce-ff36046d9870" "Publisher" "U109"
  WriteRegStr HKCU "Software\Microsoft\Windows\CurrentVersion\Uninstall\a3330349-9337-5aca-8bce-ff36046d9870" "InstallLocation" "$INSTDIR"
  WriteRegStr HKCU "Software\Microsoft\Windows\CurrentVersion\Uninstall\a3330349-9337-5aca-8bce-ff36046d9870" "UninstallString" '$\"$INSTDIR\Uninstall API Subagents.exe$\"'
  WriteRegDWORD HKCU "Software\Microsoft\Windows\CurrentVersion\Uninstall\a3330349-9337-5aca-8bce-ff36046d9870" "NoModify" 1
  WriteRegDWORD HKCU "Software\Microsoft\Windows\CurrentVersion\Uninstall\a3330349-9337-5aca-8bce-ff36046d9870" "NoRepair" 1
SectionEnd

Section "Uninstall"
  SetShellVarContext current
  ; 仅删除本安装器创建的文件，保留连接、任务记录及独立插件程序。
  Delete "$INSTDIR\API Subagents.exe"
  Delete "$INSTDIR\THIRD-PARTY-NOTICES.txt"
  Delete "$INSTDIR\Uninstall API Subagents.exe"
  RMDir "$INSTDIR"
  Delete "$DESKTOP\API Subagents.lnk"
  Delete "$SMPROGRAMS\API Subagents.lnk"
  DeleteRegKey HKCU "Software\Microsoft\Windows\CurrentVersion\Uninstall\a3330349-9337-5aca-8bce-ff36046d9870"
SectionEnd
