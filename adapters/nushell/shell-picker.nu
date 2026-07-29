def --env _shell_picker_pick_cd [] {
  let launch = (try {
    {valid: true, value: (^shell-picker cd --cwd $env.PWD --home $nu.home-dir --output nuon | complete)}
  } catch {
    {valid: false, value: null}
  })
  if not $launch.valid {
    return
  }
  let outcome = $launch.value
  let invalid = {valid: false, value: null}
  let selection = (try {
    if $outcome.exit_code != 0 {
      $invalid
    } else {
      let parsed = ($outcome.stdout | from nuon)
      if $parsed.status != "accepted" {
        $invalid
      } else {
        let paths = $parsed.paths
        if ($paths | describe) !~ '^list' or ($paths | length) != 1 {
          $invalid
        } else {
          let path = ($paths | first)
          if ($path | describe) != "string" {
            $invalid
          } else {
            {valid: true, value: $path}
          }
        }
      }
    }
  } catch {
    $invalid
  })
  if not $selection.valid {
    return
  }

  let changed = (try {
    %cd $selection.value
    true
  } catch {
    false
  })
  if not $changed {
    return
  }
  commandline edit --replace ""
}

def --env _shell_picker_pick_cp [] {
  let launch = (try {
    {valid: true, value: (^shell-picker cp --cwd $env.PWD --home $nu.home-dir --output nuon | complete)}
  } catch {
    {valid: false, value: null}
  })
  if not $launch.valid {
    return
  }
  let outcome = $launch.value
  let invalid = {valid: false, value: null}
  let selection = (try {
    if $outcome.exit_code != 0 {
      $invalid
    } else {
      let parsed = ($outcome.stdout | from nuon)
      if $parsed.status != "accepted" {
        $invalid
      } else {
        let paths = $parsed.paths
        if ($paths | describe) !~ '^list' or ($paths | is-empty) {
          $invalid
        } else if ($paths | any {|path| ($path | describe) != "string" }) {
          $invalid
        } else {
          {valid: true, value: $paths}
        }
      }
    }
  } catch {
    $invalid
  })
  if not $selection.valid {
    return
  }

  let encoded = (try {
    {valid: true, value: ($selection.value | each {|path| $path | to nuon --raw } | str join " ")}
  } catch {
    $invalid
  })
  if not $encoded.valid {
    return
  }
  commandline edit --replace $"cp ($encoded.value)"
}

def --env _shell_picker_space [] {
  let buffer = (commandline)
  let cursor = (commandline get-cursor)
  commandline edit --insert " "

  if $cursor != 2 {
    return
  }
  if $buffer == "cd" {
    _shell_picker_pick_cd
  } else if $buffer == "cp" {
    _shell_picker_pick_cp
  }
}

def --env shell-picker-bind-nushell [] {
  let binding = {
    name: _shell_picker_space
    modifier: none
    keycode: space
    mode: [emacs vi_normal vi_insert]
    event: {send: executehostcommand, cmd: "_shell_picker_space"}
  }
  $env.config.keybindings = (
    $env.config.keybindings
    | where {|existing| try { $existing.name != "_shell_picker_space" } catch { true } }
    | append $binding
  )
}
