@{
    RootModule = 'shell-picker.psm1'
    ModuleVersion = '1.0.0'
    GUID = '2b6bc4d1-1e92-4b6a-a208-66e273cdde43'
    Author = 'shell-picker contributors'
    Description = 'Direct PSReadLine adapter for shell-picker.'
    PowerShellVersion = '7.4.7'
    CompatiblePSEditions = @('Core')
    RequiredModules = @(
        @{
            ModuleName = 'PSReadLine'
            ModuleVersion = '2.3.6'
        }
    )
    FunctionsToExport = @('Register-ShellPicker')
    CmdletsToExport = @()
    VariablesToExport = @()
    AliasesToExport = @()
}
