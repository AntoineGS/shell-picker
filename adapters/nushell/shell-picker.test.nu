use std/assert

# Reedline is unavailable to noninteractive tests. These commands preserve the
# production commandline API while providing deterministic editor state.
def commandline [] {
  $env.SHELL_PICKER_TEST_BUFFER
}

def "commandline get-cursor" [] {
  $env.SHELL_PICKER_TEST_CURSOR
}

def --env "commandline edit" [text: string, --append, --insert, --replace] {
  let operation = if $insert { "insert" } else if $append { "append" } else { "replace" }
  $env.SHELL_PICKER_TEST_EDITS = ($env.SHELL_PICKER_TEST_EDITS | append $operation)
  if $insert {
    let cursor = $env.SHELL_PICKER_TEST_CURSOR
    let buffer = ($env.SHELL_PICKER_TEST_BUFFER | encode utf-8)
    let left = ($buffer | bytes at 0..<$cursor | decode utf-8)
    let right = ($buffer | bytes at $cursor.. | decode utf-8)
    $env.SHELL_PICKER_TEST_BUFFER = $left + $text + $right
    $env.SHELL_PICKER_TEST_CURSOR = $cursor + ($text | encode utf-8 | bytes length)
  } else if $append {
    $env.SHELL_PICKER_TEST_BUFFER = $env.SHELL_PICKER_TEST_BUFFER + $text
    $env.SHELL_PICKER_TEST_CURSOR = ($env.SHELL_PICKER_TEST_BUFFER | encode utf-8 | bytes length)
  } else {
    $env.SHELL_PICKER_TEST_BUFFER = $text
    $env.SHELL_PICKER_TEST_CURSOR = ($text | encode utf-8 | bytes length)
  }
}

source shell-picker.nu

def --env reset_editor [buffer: string, cursor: int] {
  $env.SHELL_PICKER_TEST_BUFFER = $buffer
  $env.SHELL_PICKER_TEST_CURSOR = $cursor
  $env.SHELL_PICKER_TEST_EDITS = []
}

def --env set_picker [stdout: string, exit_code: int = 0] {
  $env.SHELL_PICKER_TEST_STDOUT = $stdout
  $env.SHELL_PICKER_TEST_EXIT = ($exit_code | into string)
  rm --force $env.SHELL_PICKER_TEST_CALLS
}

def picker_calls [] {
  if not ($env.SHELL_PICKER_TEST_CALLS | path exists) {
    return []
  }
  open --raw $env.SHELL_PICKER_TEST_CALLS | lines | each {|line| $line | from nuon }
}

def assert_one_picker_call [operation: string, cwd: string] {
  let calls = (picker_calls)
  assert equal ($calls | length) 1
  assert equal $calls.0.args [$operation --cwd $cwd --home $nu.home-dir --output nuon]
  assert equal $calls.0.pwd $cwd
  assert equal $calls.0.home $nu.home-dir
}

def test_supported_version [] {
  let parts = ((version).version | split row "." | first 3 | each {|part| $part | into int })
  let supported = ($parts.0 > 0 or ($parts.0 == 0 and ($parts.1 > 113 or ($parts.1 == 113 and $parts.2 >= 1))))
  assert $supported $"Nushell 0.113.1+ required, got ((version).version)"
}

def --env test_space_trigger_matrix [] {
  for case in [
    {buffer: "cd", cursor: 2, picker: "cd", expected: "cd "},
    {buffer: "cp", cursor: 2, picker: "cp", expected: "cp "},
    {buffer: " cd", cursor: 3, picker: "", expected: " cd "},
    {buffer: "cd x", cursor: 4, picker: "", expected: "cd x "},
    {buffer: "cp", cursor: 1, picker: "", expected: "c p"},
    {buffer: "", cursor: 0, picker: "", expected: " "},
    {buffer: "écd", cursor: 2, picker: "", expected: "é cd"},
  ] {
    reset_editor $case.buffer $case.cursor
    set_picker '{status:cancelled,paths:[]}'
    let cwd = $env.PWD
    _shell_picker_space
    let calls = (picker_calls)
    if $case.picker == "" {
      assert equal $env.SHELL_PICKER_TEST_BUFFER $case.expected
      assert equal ($calls | length) 0
      assert equal $env.SHELL_PICKER_TEST_EDITS [insert]
    } else {
      assert equal $env.SHELL_PICKER_TEST_BUFFER ($case.buffer + " ")
      assert_one_picker_call $case.picker $cwd
      assert equal $env.SHELL_PICKER_TEST_EDITS [insert]
    }
    assert equal $env.SHELL_PICKER_TEST_CURSOR ($case.cursor + 1)
  }
}

def --env test_cd_accepts_one_string_with_percent_cd [] {
  let original = $env.PWD
  let target = ($env.SHELL_PICKER_TEST_ROOT | path join "cd target with space" "nested")
  mkdir $target
  let output = ({status: accepted, paths: [$target]} | to nuon --raw)
  reset_editor "cd" 2
  set_picker $output
  _shell_picker_space
  assert equal $env.PWD $target
  assert equal $env.SHELL_PICKER_TEST_BUFFER ""
  assert equal $env.SHELL_PICKER_TEST_EDITS [insert replace]
  assert_one_picker_call cd $original
  %cd $original
}

def --env test_cp_nuon_insertion [] {
  let paths = ["first path", $"quote(char dq)path", $"line(char nl)path", "first path", "back\\slash", "é path"]
  let output = ({status: accepted, paths: $paths} | to nuon --raw)
  let cwd = $env.PWD
  reset_editor "cp" 2
  set_picker $output
  _shell_picker_space
  let expected = ('cp "first path" "quote\"path" "line' + (char nl) + 'path" "first path" "back\\slash" "é path"')
  assert equal $env.SHELL_PICKER_TEST_BUFFER $expected
  assert not ($env.SHELL_PICKER_TEST_BUFFER | str ends-with " ")
  let encoded = ($env.SHELL_PICKER_TEST_BUFFER | str substring 3..)
  assert equal ($"[($encoded)]" | from nuon) $paths
  assert equal $env.SHELL_PICKER_TEST_EDITS [insert replace]
  assert_one_picker_call cp $cwd
}

def --env test_soft_cd_and_cp_results [] {
  let original = $env.PWD
  let missing = ($env.SHELL_PICKER_TEST_ROOT | path join "does not exist")
  let accepted_missing = ({status: accepted, paths: [$missing]} | to nuon --raw)
  let malformed = [
    {name: abort, stdout: '{status:cancelled,paths:[]}', exit_code: 0},
    {name: nonzero, stdout: '{status:accepted,paths:["ignored"]}', exit_code: 7},
    {name: invalid, stdout: '{', exit_code: 0},
    {name: scalar, stdout: '42', exit_code: 0},
    {name: missing-status, stdout: '{paths:["ignored"]}', exit_code: 0},
    {name: wrong-status, stdout: '{status:42,paths:["ignored"]}', exit_code: 0},
    {name: missing-paths, stdout: '{status:accepted}', exit_code: 0},
    {name: scalar-paths, stdout: '{status:accepted,paths:"ignored"}', exit_code: 0},
    {name: null-paths, stdout: '{status:accepted,paths:null}', exit_code: 0},
    {name: member-type, stdout: '{status:accepted,paths:["ok",42]}', exit_code: 0},
    {name: cd-empty, stdout: '{status:accepted,paths:[]}', exit_code: 0},
    {name: cd-many, stdout: '{status:accepted,paths:["one","two"]}', exit_code: 0},
    {name: cd-failure, stdout: $accepted_missing, exit_code: 0},
  ]

  for case in $malformed {
    reset_editor "cd" 2
    set_picker $case.stdout $case.exit_code
    _shell_picker_space
    assert equal $env.SHELL_PICKER_TEST_BUFFER "cd " $case.name
    assert equal $env.PWD $original $case.name
    assert equal $env.SHELL_PICKER_TEST_EDITS [insert] $case.name
    assert_one_picker_call cd $original
  }

  for case in ($malformed | where name != cd-many and name != cd-failure) {
    reset_editor "cp" 2
    set_picker $case.stdout $case.exit_code
    _shell_picker_space
    assert equal $env.SHELL_PICKER_TEST_BUFFER "cp " $case.name
    assert equal $env.PWD $original $case.name
    assert equal $env.SHELL_PICKER_TEST_EDITS [insert] $case.name
    assert_one_picker_call cp $original
  }
}

def --env test_picker_spawn_failures_are_soft [] {
  let saved_path = $env.PATH
  let original = $env.PWD
  mut results = []
  for case in [
    {name: absent, path: $env.SHELL_PICKER_TEST_EMPTY_BIN},
    {name: unlaunchable, path: $env.SHELL_PICKER_TEST_UNLAUNCHABLE_BIN},
  ] {
    $env.PATH = [$case.path]
    for operation in [cd cp] {
      reset_editor $operation 2
      set_picker ""
      let succeeded = (try {
        _shell_picker_space
        true
      } catch {
        false
      })
      $results = ($results | append {
        name: $"($operation)-($case.name)"
        expected: ($operation + " ")
        succeeded: $succeeded
        buffer: $env.SHELL_PICKER_TEST_BUFFER
        cwd: $env.PWD
        edits: $env.SHELL_PICKER_TEST_EDITS
        calls: ((picker_calls) | length)
      })
    }
  }
  $env.PATH = $saved_path

  for result in $results {
    assert $result.succeeded $result.name
    assert equal $result.buffer $result.expected $result.name
    assert equal $result.cwd $original $result.name
    assert equal $result.edits [insert] $result.name
    assert equal $result.calls 0 $result.name
  }
}

def --env test_binding_is_targeted_and_idempotent [] {
  let tab = {
    name: completion_menu
    modifier: none
    keycode: tab
    mode: [emacs vi_normal vi_insert]
    event: {until: [{send: menu, name: completion_menu}, {edit: complete}]}
  }
  let control_space = {
    name: ide_completion_menu
    modifier: control
    keycode: space
    mode: [emacs vi_normal vi_insert]
    event: {send: menu, name: ide_completion_menu}
  }
  let unrelated = {name: unrelated, modifier: shift, keycode: char_x, mode: emacs, event: {edit: undo}}
  let old_one = {name: _shell_picker_space, modifier: none, keycode: space, mode: emacs, event: {edit: complete}}
  let old_two = {name: _shell_picker_space, modifier: shift, keycode: space, mode: vi_insert, event: {edit: undo}}
  $env.config = {keybindings: [$tab $control_space $unrelated $old_one $old_two]}
  let tab_before = (($env.config.keybindings | where keycode == tab | first) | to nuon --raw)
  let control_space_before = (($env.config.keybindings | where name == ide_completion_menu | first) | to nuon --raw)

  shell-picker-bind-nushell
  let first = ($env.config.keybindings | to nuon --raw)
  let own = ($env.config.keybindings | where name == _shell_picker_space)
  assert equal ($own | length) 1
  assert equal $own.0.modifier none
  assert equal $own.0.keycode space
  assert equal $own.0.mode [emacs vi_normal vi_insert]
  assert equal $own.0.event {send: executehostcommand, cmd: "_shell_picker_space"}
  assert equal (($env.config.keybindings | where keycode == tab | first) | to nuon --raw) $tab_before
  assert equal (($env.config.keybindings | where name == ide_completion_menu | first) | to nuon --raw) $control_space_before
  assert equal (($env.config.keybindings | where name == unrelated) | length) 1

  shell-picker-bind-nushell
  assert equal ($env.config.keybindings | to nuon --raw) $first
  assert equal (($env.config.keybindings | where name == _shell_picker_space) | length) 1
}

let test_root = (($env.TMPDIR? | default $nu.temp-dir) | path join $"shell-picker-nushell-(random uuid)")
let bin = ($test_root | path join "bin")
mkdir $bin
let fake = ($test_root | path join "fake-shell-picker.nu")
r#'def --wrapped main [...args: string] {
  let call = {args: $args, pwd: $env.PWD, home: $nu.home-dir}
  (($call | to nuon --raw) + (char nl)) | save --append $env.SHELL_PICKER_TEST_CALLS
  print --no-newline $env.SHELL_PICKER_TEST_STDOUT
  exit ($env.SHELL_PICKER_TEST_EXIT | into int)
}
'# | save $fake

let empty_bin = ($test_root | path join "empty-bin")
let unlaunchable_bin = ($test_root | path join "unlaunchable-bin")
mkdir $empty_bin $unlaunchable_bin
if $nu.os-info.name == "windows" {
  "not a Windows executable" | save ($unlaunchable_bin | path join "shell-picker.exe")
} else {
  let unlaunchable = ($unlaunchable_bin | path join "shell-picker")
  "not an executable" | save $unlaunchable
  ^chmod +x $unlaunchable
}

$env.SHELL_PICKER_TEST_ROOT = $test_root
$env.SHELL_PICKER_TEST_CALLS = ($test_root | path join "calls.nuon")
$env.SHELL_PICKER_TEST_FAKE = $fake
$env.SHELL_PICKER_TEST_NU = $nu.current-exe
$env.SHELL_PICKER_TEST_EMPTY_BIN = $empty_bin
$env.SHELL_PICKER_TEST_UNLAUNCHABLE_BIN = $unlaunchable_bin
$env.PATH = ($env.PATH | prepend $bin)

if $nu.os-info.name == "windows" {
  "@echo off\r\n\"%SHELL_PICKER_TEST_NU%\" --no-config-file \"%SHELL_PICKER_TEST_FAKE%\" %*\r\n" | save ($bin | path join "shell-picker.cmd")
} else {
  "#!/bin/sh\nexec \"$SHELL_PICKER_TEST_NU\" --no-config-file \"$SHELL_PICKER_TEST_FAKE\" \"$@\"\n" | save ($bin | path join "shell-picker")
  ^chmod +x ($bin | path join "shell-picker")
}

test_supported_version
test_space_trigger_matrix
test_cd_accepts_one_string_with_percent_cd
test_cp_nuon_insertion
test_soft_cd_and_cp_results
test_picker_spawn_failures_are_soft
test_binding_is_targeted_and_idempotent

rm --recursive --force $test_root
print "nushell adapter tests: PASS"
