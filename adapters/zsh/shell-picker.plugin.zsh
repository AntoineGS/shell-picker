_shell_picker_cd() {
  emulate -L zsh
  local saved=$BUFFER saved_cursor=$CURSOR output target record
  local -a selected_paths
  integer picker_status=1 count=0 valid=1

  output=$(command mktemp -- "${TMPDIR:-/tmp}/shell-picker-cd.${$}.XXXXXX") || {
    zle redisplay
    return 0
  }
  {
    command shell-picker cd --cwd "$PWD" --home "$HOME" --output nul >| "$output"
    picker_status=$?
    while true; do
      record=
      if IFS= read -r -d '' record; then
        [[ -n $record ]] || valid=0
        target=$record
        selected_paths+=("$record")
        (( ++count ))
      else
        [[ -z $record ]] || valid=0
        break
      fi
    done < "$output"
  } always {
    command rm -f -- "$output"
  }

  if (( picker_status == 10 && valid && count > 0 )); then
    _shell_picker_insert_paths "${selected_paths[@]}"
    return 0
  fi
  if (( picker_status != 0 || ! valid || count != 1 )); then
    BUFFER=$saved
    CURSOR=$saved_cursor
    zle redisplay
    return 0
  fi
  BUFFER="builtin cd -- ${(q)target}"
  zle accept-line
  return 0
}

_shell_picker_cp() {
  emulate -L zsh
  local saved=$BUFFER saved_cursor=$CURSOR output selected word argument cluster option redirection
  local -a selected_paths quoted parsed_words command_words
  integer picker_status=1 valid=1 has_terminator=0 index option_index
  integer expect_redirection_operand=0 expect_option_argument=0

  output=$(command mktemp -- "${TMPDIR:-/tmp}/shell-picker-cp.${$}.XXXXXX") || {
    zle redisplay
    return 0
  }
  {
    command shell-picker cp --cwd "$PWD" --home "$HOME" --output nul >| "$output"
    picker_status=$?
    while true; do
      selected=
      if IFS= read -r -d '' selected; then
        [[ -n $selected ]] || valid=0
        selected_paths+=("$selected")
      else
        [[ -z $selected ]] || valid=0
        break
      fi
    done < "$output"
  } always {
    command rm -f -- "$output"
  }

  if (( picker_status == 10 && valid && ${#selected_paths} > 0 )); then
    _shell_picker_insert_paths "${selected_paths[@]}"
    return 0
  fi
  if (( picker_status != 0 || ! valid || ${#selected_paths} == 0 )); then
    BUFFER=$saved
    CURSOR=$saved_cursor
    zle redisplay
    return 0
  fi
  for selected in "${selected_paths[@]}"; do
    quoted+=("${(q)selected}")
  done
  parsed_words=( ${(z)LBUFFER} )
  for word in $parsed_words; do
    case $word in
      ';' | '&' | '&&' | '||' | '|' | '|&' | '&!' | '&|') command_words=() ;;
      *) command_words+=("$word") ;;
    esac
  done
  if [[ $command_words[1] == cp ]]; then
    for (( index = 2; index <= ${#command_words}; ++index )); do
      word=${command_words[index]}
      if (( expect_redirection_operand )); then
        expect_redirection_operand=0
        continue
      fi
      redirection=$word
      [[ $redirection == <->* ]] && redirection=${redirection##<->}
      case $redirection in
        '<' | '<>' | '<&' | '>' | '>|' | '>>' | '>>|' | '>&' | '>>&' | '>>&|' | \
          '&>' | '&>|' | '<<' | '<<-' | '<<<')
          expect_redirection_operand=1
          continue
          ;;
      esac
      argument=${(Q)word}
      if (( expect_option_argument )); then
        expect_option_argument=0
        continue
      fi
      if [[ $argument == -- ]]; then
        has_terminator=1
        break
      fi
      if [[ $argument == --target-directory || $argument == --suffix ]]; then
        expect_option_argument=1
      elif [[ $argument == -?* && $argument != --* ]]; then
        cluster=${argument#-}
        for (( option_index = 1; option_index <= ${#cluster}; ++option_index )); do
          option=$cluster[option_index]
          if [[ $option == t || $option == S ]]; then
            (( option_index == ${#cluster} )) && expect_option_argument=1
            break
          fi
        done
      fi
    done
  fi
  (( has_terminator )) || quoted=(-- "${quoted[@]}")
  LBUFFER+="${(j: :)quoted}"
  zle redisplay
  return 0
}

_shell_picker_search_context_supported() {
  emulate -L zsh
  local quote='' character
  integer index

  for (( index = 1; index <= ${#LBUFFER}; ++index )); do
    character=$LBUFFER[index]
    case $character in
      $'\\' | '$' | '`' | '(' | ')') return 1 ;;
    esac
    if [[ $quote == "'" ]]; then
      [[ $character == "'" ]] && quote=''
    elif [[ $quote == '"' ]]; then
      [[ $character == '"' ]] && quote=''
    else
      case $character in
        "'") quote="'" ;;
        '"') quote='"' ;;
      esac
    fi
  done
  [[ -z $quote ]]
}

_shell_picker_search() {
  emulate -L zsh
  local saved=$BUFFER saved_cursor=$CURSOR output selected
  local -a selected_paths
  integer picker_status=1 valid=1

  if ! _shell_picker_search_context_supported; then
    zle -M 'Search insertion declined: move the cursor outside quotes, escapes, and shell expansions'
    return 0
  fi

  output=$(command mktemp -- "${TMPDIR:-/tmp}/shell-picker-search.${$}.XXXXXX") || {
    BUFFER=$saved
    CURSOR=$saved_cursor
    zle redisplay
    return 0
  }
  {
    command shell-picker search --cwd "$PWD" --home "$HOME" --output nul >| "$output"
    picker_status=$?
    while true; do
      selected=
      if IFS= read -r -d '' selected; then
        if [[ -n $selected && $selected == /* ]]; then
          selected_paths+=("$selected")
        else
          valid=0
        fi
      else
        [[ -z $selected ]] || valid=0
        break
      fi
    done < "$output"
  } always {
    command rm -f -- "$output"
  }

  if (( picker_status != 0 || ! valid || ${#selected_paths} == 0 )); then
    BUFFER=$saved
    CURSOR=$saved_cursor
    zle redisplay
    return 0
  fi
  _shell_picker_insert_paths "${selected_paths[@]}"
  return 0
}

_shell_picker_insert_paths() {
  emulate -L zsh
  local selected insertion
  local -a quoted
  if ! _shell_picker_search_context_supported; then
    zle -M 'Search insertion declined: move the cursor outside quotes, escapes, and shell expansions'
    return 0
  fi
  for selected in "$@"; do
    if [[ -z $selected || $selected != /* ]]; then
      zle redisplay
      return 0
    fi
    quoted+=("${(q)selected}")
  done
  (( ${#quoted} > 0 )) || return 0
  insertion="${(j: :)quoted}"
  if [[ -n $LBUFFER && $LBUFFER[-1] != [[:space:]] ]]; then
    insertion=" $insertion"
  fi
  if [[ -n $RBUFFER && $RBUFFER[1] != [[:space:]] ]]; then
    insertion+=" "
  fi
  LBUFFER+="$insertion"
  zle redisplay
  return 0
}

_shell_picker_tab() {
  emulate -L zsh
  local word marker="__shell_picker_cp_marker_${$}_${RANDOM}__"
  local input=$LBUFFER$marker
  local -a parsed_words command_words
  parsed_words=( ${(z)input} )
  if [[ -n $RBUFFER || $parsed_words[-1] != $marker ]]; then
    zle fzf_completion
    return 0
  fi
  parsed_words[-1]=()
  for word in $parsed_words; do
    case $word in
      ';' | '&' | '&&' | '||' | '|' | '|&' | '&!' | '&|') command_words=() ;;
      *) command_words+=("$word") ;;
    esac
  done
  if [[ $command_words[1] == cp ]]; then
    zle _shell_picker_cp
  else
    zle fzf_completion
  fi
  return 0
}

_shell_picker_space() {
  emulate -L zsh
  zle magic-space
  if [[ $BUFFER == "cd " && $CURSOR -eq 3 ]]; then
    zle _shell_picker_cd
  fi
  return 0
}

shell-picker-bind-zsh() {
  bindkey ' ' _shell_picker_space
  bindkey '^I' _shell_picker_tab
  bindkey -M emacs '^F' _shell_picker_search
  bindkey -M viins '^F' _shell_picker_search
  bindkey -M vicmd '^F' _shell_picker_search
  # Ctrl+Shift+F mapped by the terminal to ESC Ctrl+F.
  bindkey -M emacs $'\e\x06' _shell_picker_search
  bindkey -M viins $'\e\x06' _shell_picker_search
  bindkey -M vicmd $'\e\x06' _shell_picker_search
}

zle -N _shell_picker_cd
zle -N _shell_picker_cp
zle -N _shell_picker_search
zle -N _shell_picker_tab
zle -N _shell_picker_space
