#!/bin/bash

# This script is meant to be sourced from .bashrc. To install it, run:
#
# curl -s https://raw.githubusercontent.com/smxlong/dump/main/bash/bash-enhancements-install.sh | bash

# BASH_ENHANCEMENTS_DEBUG is the debug mode flag.
if [[ -n "${BASH_ENHANCEMENTS_DEBUG:-}" ]]; then
    set -x  # Enable debug mode
    echo "Debug mode is ON"
else
    set +x  # Disable debug mode
fi

# If shell is interactive, enable color, if not disabled with BASH_ENHANCEMENTS_COLOR=0.
if [[ $- == *i* ]] && [[ "${BASH_ENHANCEMENTS_COLOR:-1}" -ne 0 ]]; then
    BASH_ENHANCEMENTS_COLOR=1
    # Enable color support for ls and grep
    export CLICOLOR=1
    export GREP_OPTIONS='--color=auto'
    export LS_COLORS='di=0;34:ln=0;36:so=0;35:pi=0;33:ex=0;32:bd=0;33;01:cd=0;33;01:su=0;37;41:sg=0;30;43:tw=0;34;42:ow=0;34;42'
    # Enable git prompt colors
    export GIT_PS1_SHOWCOLORHINTS=1
else
    BASH_ENHANCEMENTS_COLOR=0
    # If not interactive, disable color support
    export CLICOLOR=0
    export GREP_OPTIONS=''
    export LS_COLORS=''
    # Disable git prompt colors
    unset GIT_PS1_SHOWCOLORHINTS
fi

# Create a good prompt.

# Prompt component functions
prompt_status_emoji() {
    local last_exit=$?
    if [[ $BASH_ENHANCEMENTS_COLOR -eq 1 ]]; then
        if [[ $last_exit -eq 0 ]]; then echo "😎"; else echo "😞"; fi
    else
        if [[ $last_exit -eq 0 ]]; then echo ":)"; else echo ":("; fi
    fi
}

prompt_chroot_and_user() {
    if [[ $BASH_ENHANCEMENTS_COLOR -eq 1 ]]; then
        echo '${debian_chroot:+($debian_chroot)}\[\033[01;32m\]$(prompt_status_emoji)\n==== \u@\h\[\033[00m\]'
    else
        echo '${debian_chroot:+($debian_chroot)}$(prompt_status_emoji)\n==== \u@\h'
    fi
}

prompt_git_info() {
    echo '$(__git_ps1 "%s")'
}

prompt_working_directory() {
    if [[ $BASH_ENHANCEMENTS_COLOR -eq 1 ]]; then
        echo '\[\033[01;34m\]\w\[\033[00m\] \$ '
    else
        echo '\w \$ '
    fi
}

# Assemble the full prompt
PS1="$(prompt_chroot_and_user)\n$(prompt_git_info)\n$(prompt_working_directory)"
