# Build qg from this checkout and put it on PATH. Run VHS from the repository root.
# Provide a temporary HOME, XDG_CONFIG_HOME, XDG_CACHE_HOME, XDG_DATA_HOME and
# XDG_STATE_HOME. Use a short temporary XDG_RUNTIME_DIR for Unix sockets.
# Set QG_DEMO_ISOLATED=1 only after preparing those isolated directories.
# Pass QG_API_KEY through the VHS process environment. Never type it in a tape.
# Render with vhs -o "$TMPDIR/qg-preview.gif" docs/assets/tapes/demo-inicio.tape.
# Run demo-usage.tape after demo-sessao.tape to reuse the latest conversation.
Require qg
Set Shell "bash"
Set Width 1400
Set Height 860
Set FontSize 20
Set FontFamily "Menlo"
Set LineHeight 1.1
Set Padding 24
Set Margin 0
Set BorderRadius 0
Set CursorBlink false
Set TypingSpeed 0ms
Set WaitTimeout 120s
Set Theme { "background": "#0b0b0d", "foreground": "#e4e4e7", "cursor": "#e4e4e7", "selection": "#303036" }
