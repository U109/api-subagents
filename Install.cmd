@echo off
powershell.exe -NoProfile -ExecutionPolicy Bypass -File "%~dp0scripts\source-install.ps1" %*
set "INSTALL_EXIT=%ERRORLEVEL%"
pause
exit /b %INSTALL_EXIT%
