Source docs/assets/tapes/settings.tape
# The full root help needs more rows; the width stays identical.
Set Height 1700
Source docs/assets/tapes/setup.tape
Type "qg --help"
Sleep 300ms
Enter
Wait+Screen /skip-permissions-unsafe/
Sleep 500ms
Show
Sleep 500ms
Screenshot docs/assets/demo-help.png
