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

# If shell is interactive, enable color
if [[ $- == *i* ]]; then
    BASH_ENHANCEMENTS_COLOR=1
    # Enable color support for ls and grep
    export CLICOLOR=1
    export GREP_OPTIONS='--color=auto'
    export LS_COLORS='di=0;34:ln=0;36:so=0;35:pi=0;33:ex=0;32:bd=0;33;01:cd=0;33;01:su=0;37;41:sg=0;30;43:tw=0;34;42:ow=0;34;42'
else
    BASH_ENHANCEMENTS_COLOR=0
    # If not interactive, disable color support
    export CLICOLOR=0
    export GREP_OPTIONS=''
    export LS_COLORS=''
fi

# Create a good prompt.

if [[ -n "${BASH_ENHANCEMENTS_COLOR:-}" && $BASH_ENHANCEMENTS_COLOR -eq 1 ]]; then
    PS1='${debian_chroot:+($debian_chroot)}\[\033[01;32m\]$(if [[ $? -eq 0 ]]; then echo "😎"; else echo "😞"; fi)\n==== \u@\h\[\033[00m\]\n$(__git_ps1 "%s")\n\[\033[01;34m\]\w\[\033[00m\] \$ '
else
    PS1='${debian_chroot:+($debian_chroot)}$(if [[ $? -eq 0 ]]; then echo ":)"; else echo ":("; fi) \u@\h\n$(__git_ps1 "%s")\n\w \$ '
fi
