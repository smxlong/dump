#!/bin/bash

set -euo pipefail

# This script installs the bash-enhancements.sh script, and optionally configures
# it to be updated periodically.

SCRIPT_URL="https://raw.githubusercontent.com/smxlong/dump/main/bash/bash-enhancements.sh"
INSTALL_PATH="$HOME/.bash-enhancements.sh"
BASHRC_PATH="$HOME/.bashrc"

echo "Installing bash-enhancements.sh..."

# Check if already installed
if [ -f "$INSTALL_PATH" ]; then
    echo "Script already exists at $INSTALL_PATH"
else
    # Download the script
    echo "Downloading from $SCRIPT_URL..."
    curl -fsSL "$SCRIPT_URL" -o "$INSTALL_PATH"
    chmod +x "$INSTALL_PATH"
    echo "Downloaded and made executable"
fi

# Check if already configured in .bashrc
if grep -q "bash-enhancements.sh" "$BASHRC_PATH" 2>/dev/null; then
    echo "Already configured in .bashrc"
else
    echo "Adding to .bashrc..."
    echo "" >> "$BASHRC_PATH"
    echo "# Source bash enhancements" >> "$BASHRC_PATH"
    echo "source ~/.bash-enhancements.sh" >> "$BASHRC_PATH"
    echo "Added to .bashrc"
fi

echo "Installation complete! Restart your terminal or run 'source ~/.bashrc' to activate."
