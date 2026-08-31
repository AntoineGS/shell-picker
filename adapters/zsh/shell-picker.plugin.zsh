_shell_picker_cd() {
  emulate -L zsh
  local saved=$BUFFER saved_cursor=$CURSOR target
  local -a records
  integer picker_status=1 valid=1

  records=("${(@0)$(
    command shell-picker cd --cwd "$PWD" --home "$HOME" --output nul
    picker_status=$?
    builtin print -rn -- $'\0\0'
    exit $picker_status
  )}")
  picker_status=$?
  if [[ ${records[-1]} == '' && ${records[-2]} == '' ]]; then
    records[-1]=()
    records[-1]=()
  else
    valid=0
  fi
  if [[ ${records[-1]} == '' ]]; then
    records[-1]=()
  else
    valid=0
  fi
  (( ${#records} == 1 )) || valid=0
  for target in "${records[@]}"; do
    [[ -n $target ]] || valid=0
  done

  if (( picker_status != 0 || ! valid )); then
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
  local saved=$BUFFER saved_cursor=$CURSOR selected word argument cluster option redirection
  local -a selected_paths quoted parsed_words command_words
  integer picker_status=1 valid=1 has_terminator=0 index option_index
  integer expect_redirection_operand=0 expect_option_argument=0

  selected_paths=("${(@0)$(
    command shell-picker cp --cwd "$PWD" --home "$HOME" --output nul
    picker_status=$?
    builtin print -rn -- $'\0\0'
    exit $picker_status
  )}")
  picker_status=$?
  if [[ ${selected_paths[-1]} == '' && ${selected_paths[-2]} == '' ]]; then
    selected_paths[-1]=()
    selected_paths[-1]=()
  else
    valid=0
  fi
  if [[ ${selected_paths[-1]} == '' ]]; then
    selected_paths[-1]=()
  else
    valid=0
  fi
  for selected in "${selected_paths[@]}"; do
    [[ -n $selected ]] || valid=0
  done

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
}

zle -N _shell_picker_cd
zle -N _shell_picker_cp
zle -N _shell_picker_tab
zle -N _shell_picker_space
