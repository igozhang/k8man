$ErrorActionPreference = "Continue"
# 兼容入口：转发到 %TEMP% 打包脚本，不污染 git 工作区
& "$PSScriptRoot\_build_pipeline.ps1"
