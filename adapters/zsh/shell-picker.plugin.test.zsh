#!/usr/bin/env zsh

emulate -LR zsh

typeset -gi failures=0 accepted_calls=0 redisplay_calls=0 fzf_completion_calls=0
typeset -ga registered_widgets=() bound_keys=() events=()

fail() {
  print -ru2 -- "FAIL: $1"
  (( ++failures ))
}

assert_equal() {
  local want=$1 got=$2 message=$3
  [[ $got == $want ]] || fail "$message: got ${(qqq)got}, want ${(qqq)want}"
}

zle() {
  local widget=$1
  case $widget in
    -N)
      registered_widgets+=("$2")
      ;;
    magic-space)
      events+=(magic-space)
      BUFFER="${BUFFER[1,CURSOR]} ${BUFFER[$(( CURSOR + 1 )),-1]}"
      (( ++CURSOR ))
      ;;
    accept-line)
      events+=(accept-line)
      (( ++accepted_calls ))
      ;;
    redisplay)
      events+=(redisplay)
      (( ++redisplay_calls ))
      ;;
    fzf_completion)
      events+=(fzf_completion)
      (( ++fzf_completion_calls ))
      ;;
    _shell_picker_cd | _shell_picker_cp)
      events+=("$widget")
      "$widget"
      ;;
    *)
      fail "unexpected zle call ${(qqq)widget}"
      ;;
  esac
}

bindkey() {
  bound_keys+=("$1:$2")
}

plugin=${0:A:h}/shell-picker.plugin.zsh
source "$plugin" || exit $?

test_root=$(mktemp -d "${TMPDIR:-/tmp}/shell-picker-zsh-test.XXXXXX") || exit 1
trap 'rm -rf -- "$test_root"' EXIT
mkdir "$test_root/bin" "$test_root/tmp"
export PICKER_CALLS=$test_root/calls PICKER_MODE=abort TMPDIR=$test_root/tmp
: >| "$PICKER_CALLS"

cat >| "$test_root/bin/shell-picker" <<'FAKE'
#!/usr/bin/env zsh
emulate -LR zsh
print -r -- "$$" >> "$PICKER_CALLS"
[[ $# == 7 && ($1 == cd || $1 == cp) && $2 == --cwd && $3 == "$PWD" && $4 == --home && $5 == "$HOME" && $6 == --output && $7 == nul ]] || exit 91
emit() { print -rn -- "$1"$'\0' }
case $PICKER_MODE in
  abort) ;;
  error) exit 1 ;;
  cd-newline) emit $'line\n' ;;
  cd-double) emit one; emit two ;;
  cp-order) emit 'first path'; emit 'third path'; emit 'first path' ;;
  cp-leading) emit '-leading'; emit duplicate; emit duplicate ;;
  cp-special) emit '-leading'; emit 'trailing '; emit 'back\slash'; emit "apost'rophe"; emit $'nbsp\u00a0path'; emit $'tab\tpath'; emit $'line\npath' ;;
  malformed) emit 'first path'; print -rn -- unterminated ;;
  *) print -rn -- unknown; exit 0 ;;
esac
FAKE
chmod +x "$test_root/bin/shell-picker"
export PATH=$test_root/bin:$PATH

reset_case() {
  accepted_calls=0
  redisplay_calls=0
  fzf_completion_calls=0
  events=()
  PICKER_MODE=abort
  : >| "$PICKER_CALLS"
}

picker_call_count() {
  local content=$(<"$PICKER_CALLS")
  local -a calls=( ${(f)content} )
  REPLY=${#calls}
}

test_registration_and_explicit_bindings() {
  assert_equal '_shell_picker_cd _shell_picker_cp _shell_picker_tab _shell_picker_space' \
    "${(j: :)registered_widgets}" 'source-time widget registration changed'
  assert_equal 0 "${#bound_keys}" 'source time rebound a key'
  shell-picker-bind-zsh
  assert_equal ' :_shell_picker_space ^I:_shell_picker_tab' "${(j: :)bound_keys}" \
    'explicit binding did not bind Space and Tab'
}

test_space_exact_after_magic_space() {
  reset_case
  BUFFER=cd CURSOR=2
  _shell_picker_space
  picker_call_count
  assert_equal 'cd ' "$BUFFER" 'aborted exact cd did not retain magic-space result'
  assert_equal 3 "$CURSOR" 'aborted exact cd did not retain post-space cursor'
  assert_equal 1 "$REPLY" 'exact cd Space did not invoke one picker'
  assert_equal 'magic-space _shell_picker_cd redisplay' "${(j: :)events}" \
    'magic-space did not run before the cd trigger'

  reset_case
  BUFFER='echo cd' CURSOR=7
  _shell_picker_space
  picker_call_count
  assert_equal 'echo cd ' "$BUFFER" 'ordinary Space changed the buffer unexpectedly'
  assert_equal 0 "$REPLY" 'non-exact cd invoked the picker'
  assert_equal magic-space "${(j: :)events}" 'ordinary Space did not call magic-space only'

  reset_case
  BUFFER='cd ' CURSOR=3
  _shell_picker_space
  picker_call_count
  assert_equal 'cd  ' "$BUFFER" 'preexisting cd Space did not remain ordinary'
  assert_equal 0 "$REPLY" 'post-magic non-exact buffer invoked the picker'
}

test_cd_nul_quoting_and_immediate_accept() {
  reset_case
  PICKER_MODE=cd-newline
  BUFFER='cd ' CURSOR=3
  _shell_picker_cd
  picker_call_count
  assert_equal "builtin cd -- line\$'\\n'" "$BUFFER" 'newline cd target was not quoted byte-safely'
  assert_equal 3 "$CURSOR" 'accepted cd unexpectedly rewrote the cursor'
  assert_equal 1 "$accepted_calls" 'accepted cd did not accept immediately'
  assert_equal 1 "$REPLY" 'accepted cd did not use exactly one picker process'
  assert_equal accept-line "${(j: :)events}" 'accepted cd performed an extra ZLE action'
}

test_cd_abort_error_and_malformed_restore() {
  local mode
  for mode in abort error malformed cd-double unknown; do
    reset_case
    PICKER_MODE=$mode
    BUFFER='keep cd state' CURSOR=4
    _shell_picker_cd
    picker_call_count
    assert_equal 'keep cd state' "$BUFFER" "$mode cd output changed the buffer"
    assert_equal 4 "$CURSOR" "$mode cd output changed the cursor"
    assert_equal 0 "$accepted_calls" "$mode cd output accepted the line"
    assert_equal 1 "$REPLY" "$mode cd output did not use exactly one picker process"
  done
}

test_cp_order_duplicates_and_special_bytes() {
  reset_case
  PICKER_MODE=cp-order
  LBUFFER='cp ' RBUFFER=
  _shell_picker_cp
  picker_call_count
  assert_equal 'cp -- first\ path third\ path first\ path' "$LBUFFER" 'cp did not preserve order and duplicates'
  [[ $LBUFFER != *' ' ]] || fail 'cp ordered insertion has a trailing space'
  assert_equal 1 "$REPLY" 'cp selection did not use exactly one picker process'

  reset_case
  PICKER_MODE=cp-special
  LBUFFER='cp ' RBUFFER=
  _shell_picker_cp
  assert_equal $'cp -- -leading trailing\\  back\\\\slash apost\\\047rophe nbsp\u00a0path tab$\047\\t\047path line$\047\\n\047path' \
    "$LBUFFER" 'cp special bytes were not individually quoted'
  [[ $LBUFFER != *' ' ]] || fail 'cp special insertion has a trailing space'
}

test_cp_abort_error_and_malformed_restore() {
  local mode
  for mode in abort error malformed unknown; do
    reset_case
    PICKER_MODE=$mode
    LBUFFER='cp original ' RBUFFER='suffix'
    _shell_picker_cp
    picker_call_count
    assert_equal 'cp original ' "$LBUFFER" "$mode cp output changed LBUFFER"
    assert_equal suffix "$RBUFFER" "$mode cp output changed RBUFFER"
    assert_equal 1 "$REPLY" "$mode cp output did not use exactly one picker process"
  done
}

test_cp_option_terminator() {
  reset_case
  PICKER_MODE=cp-leading
  LBUFFER='cp -a existing ' RBUFFER=
  _shell_picker_cp
  assert_equal 'cp -a existing -- -leading duplicate duplicate' "$LBUFFER" \
    'cp did not terminate options before selected operands'

  local existing
  for existing in '--' '\--'; do
    reset_case
    PICKER_MODE=cp-leading
    LBUFFER="echo before | cp -a $existing existing " RBUFFER=
    _shell_picker_cp
    assert_equal "echo before | cp -a $existing existing -leading duplicate duplicate" "$LBUFFER" \
      "cp duplicated effective terminator $existing"
  done
}

test_cp_terminator_context() {
  local -a inputs expected
  inputs=(
    'cp existing > -- '
    'cp existing 2> -- '
    'cp existing 2>> -- '
    'cp -t -- existing '
    'cp -S -- existing '
    'cp -avt -- existing '
    'cp -t-- existing '
    'cp -S-- existing '
    'cp -tfoo -- existing '
    'cp -S.bak -- existing '
    'cp --target-directory -- existing '
    'cp --suffix -- existing '
    'cp --target-directory=-- existing '
    'cp --suffix=-- existing '
  )
  expected=(
    'cp existing > -- -- -leading duplicate duplicate'
    'cp existing 2> -- -- -leading duplicate duplicate'
    'cp existing 2>> -- -- -leading duplicate duplicate'
    'cp -t -- existing -- -leading duplicate duplicate'
    'cp -S -- existing -- -leading duplicate duplicate'
    'cp -avt -- existing -- -leading duplicate duplicate'
    'cp -t-- existing -- -leading duplicate duplicate'
    'cp -S-- existing -- -leading duplicate duplicate'
    'cp -tfoo -- existing -leading duplicate duplicate'
    'cp -S.bak -- existing -leading duplicate duplicate'
    'cp --target-directory -- existing -- -leading duplicate duplicate'
    'cp --suffix -- existing -- -leading duplicate duplicate'
    'cp --target-directory=-- existing -- -leading duplicate duplicate'
    'cp --suffix=-- existing -- -leading duplicate duplicate'
  )
  integer index
  for (( index = 1; index <= ${#inputs}; ++index )); do
    reset_case
    PICKER_MODE=cp-leading
    LBUFFER=${inputs[index]} RBUFFER=
    _shell_picker_cp
    assert_equal "${expected[index]}" "$LBUFFER" "cp terminator context ${inputs[index]} was misclassified"
    [[ $LBUFFER != *' ' ]] || fail "cp terminator context ${inputs[index]} has a trailing space"
  done

  local separator
  for separator in ';' '&' '&&' '||' '|' '|&' '&!' '&|'; do
    reset_case
    PICKER_MODE=cp-leading
    LBUFFER="cp -- prior $separator cp -t -- existing " RBUFFER=
    _shell_picker_cp
    assert_equal "cp -- prior $separator cp -t -- existing -- -leading duplicate duplicate" "$LBUFFER" \
      "separator $separator did not reset terminator parser state"
  done
}

test_cp_zsh_redirection_families() {
  local -a operators=(
    '<' '<>' '>' '>|' '>!' '>>' '>>|' '>>!'
    '<&' '>&' '>&|' '>&!' '>>&' '>>&|' '>>&!'
    '&>' '&>|' '&>!' '&>>' '&>>|' '&>>!'
    '<<' '<<-' '<<<'
  )
  local operator prefix input
  for operator in $operators; do
    for prefix in '' 2; do
      reset_case
      PICKER_MODE=cp-leading
      input="cp existing ${prefix}${operator} -- "
      LBUFFER=$input RBUFFER=
      _shell_picker_cp
      assert_equal "${input}-- -leading duplicate duplicate" "$LBUFFER" \
        "Zsh redirection ${prefix}${operator} exposed selected options"
    done
  done

  local -a ordinary=(
    'cp existing "<<<" -- '
    'cp existing \<\<\< -- '
    'cp existing "&>" -- '
    'cp existing \&\> -- '
  )
  for input in $ordinary; do
    reset_case
    PICKER_MODE=cp-leading
    LBUFFER=$input RBUFFER=
    _shell_picker_cp
    assert_equal "${input}-leading duplicate duplicate" "$LBUFFER" \
      "quoted or escaped operator text ${input} was treated as redirection"
  done

  reset_case
  PICKER_MODE=cp-leading
  LBUFFER='cp -t > first 2>> second <<< input -- existing ' RBUFFER=
  _shell_picker_cp
  assert_equal 'cp -t > first 2>> second <<< input -- existing -- -leading duplicate duplicate' "$LBUFFER" \
    'multiple redirections consumed pending cp option state'
}

test_tab_current_command_parser() {
  local separator
  for separator in ';' '&' '&&' '||' '|' '|&' '&!' '&|'; do
    reset_case
    PICKER_MODE=cp-order
    LBUFFER="echo before $separator cp " RBUFFER=
    _shell_picker_tab
    picker_call_count
    assert_equal "echo before $separator cp -- first\\ path third\\ path first\\ path" "$LBUFFER" \
      "separator $separator did not reset the current command"
    assert_equal 1 "$REPLY" "separator $separator did not route to one cp picker"
    assert_equal 0 "$fzf_completion_calls" "separator $separator fell back to completion"
  done

  reset_case
  LBUFFER='echo cp ' RBUFFER=
  _shell_picker_tab
  picker_call_count
  assert_equal 0 "$REPLY" 'non-command cp invoked the picker'
  assert_equal 1 "$fzf_completion_calls" 'non-command cp did not use existing completion'

  reset_case
  LBUFFER='echo x | cp ' RBUFFER=suffix
  _shell_picker_tab
  picker_call_count
  assert_equal 'echo x | cp ' "$LBUFFER" 'RBUFFER fallback changed LBUFFER'
  assert_equal suffix "$RBUFFER" 'RBUFFER fallback corrupted its suffix'
  assert_equal 0 "$REPLY" 'RBUFFER fallback invoked the picker'
  assert_equal 1 "$fzf_completion_calls" 'RBUFFER did not use existing completion'
}

test_temp_cleanup_is_owned_and_soft() {
  reset_case
  local unrelated=$TMPDIR/shell-picker-cd.UNRELATED
  print -r -- keep >| "$unrelated"
  BUFFER='safe state' CURSOR=5
  _shell_picker_cd
  [[ -f $unrelated ]] || fail 'widget cleanup deleted an unrelated temporary file'
  local -a leftovers=( $TMPDIR/shell-picker-(cd|cp).*(N) )
  assert_equal 1 "${#leftovers}" 'widget leaked its temporary output'

  local saved_tmpdir=$TMPDIR
  TMPDIR=$test_root/missing
  BUFFER='mktemp state' CURSOR=6
  _shell_picker_cd 2>/dev/null
  TMPDIR=$saved_tmpdir
  assert_equal 'mktemp state' "$BUFFER" 'mktemp failure changed the buffer'
  assert_equal 6 "$CURSOR" 'mktemp failure changed the cursor'
}

test_temp_commands_ignore_hostile_functions() {
  reset_case
  local saved_tmpdir=$TMPDIR
  TMPDIR=$test_root/'-leading temp'/'space dir'
  mkdir -p -- "$TMPDIR"
  local unrelated=$TMPDIR/unrelated hostile_log=$TMPDIR/hostile-functions
  print -rn -- 'unrelated bytes' >| "$unrelated"
  mktemp() {
    print -r -- mktemp >> "$hostile_log"
    print -r -- "$unrelated"
  }
  rm() {
    print -r -- rm >> "$hostile_log"
    return 0
  }

  BUFFER='safe cd' CURSOR=4
  _shell_picker_cd
  LBUFFER='cp ' RBUFFER=
  _shell_picker_cp
  unfunction mktemp rm

  [[ ! -e $hostile_log ]] || fail 'widget invoked a hostile temp function'
  assert_equal 'unrelated bytes' "$(<"$unrelated")" 'hostile temp function redirected an unrelated file'
  local -a leftovers=( $TMPDIR/shell-picker-(cd|cp).*(N) )
  assert_equal 0 "${#leftovers}" 'qualified temp cleanup leaked widget output'
  TMPDIR=$saved_tmpdir
}

test_registration_and_explicit_bindings
test_space_exact_after_magic_space
test_cd_nul_quoting_and_immediate_accept
test_cd_abort_error_and_malformed_restore
test_cp_order_duplicates_and_special_bytes
test_cp_abort_error_and_malformed_restore
test_cp_option_terminator
test_cp_terminator_context
test_cp_zsh_redirection_families
test_tab_current_command_parser
test_temp_cleanup_is_owned_and_soft
test_temp_commands_ignore_hostile_functions

if (( failures != 0 )); then
  print -ru2 -- "$failures zsh adapter assertion(s) failed"
  exit 1
fi
print -- 'zsh adapter tests: PASS'
