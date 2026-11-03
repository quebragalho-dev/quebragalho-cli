Source docs/assets/tapes/settings.tape
Source docs/assets/tapes/setup.tape
Type "qg"
Sleep 300ms
Enter
Wait+Screen /descreva uma tarefa/
Sleep 2s
# Run after demo-sessao.tape, before starting another conversation.
Type "/resume latest"
Sleep 300ms
Enter
Wait+Screen /Pronto para começar/
Sleep 2s
Type "/usage"
Sleep 300ms
Enter
Wait+Screen /Última chamada/
Sleep 1s
Show
Sleep 1s
Screenshot docs/assets/demo-usage.png
Sleep 1s
Hide
Escape
Ctrl+C
Sleep 500ms
