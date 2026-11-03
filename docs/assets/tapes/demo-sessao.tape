Source docs/assets/tapes/settings.tape
Set Height 940
Source docs/assets/tapes/setup.tape
Type "qg"
Sleep 300ms
Enter
Wait+Screen /descreva uma tarefa/
Sleep 2s
Type "Me explica como este projeto funciona em três tópicos. Leia README.md e tarefas.py, sem executar comandos. Termine com a frase Pronto para começar."
Sleep 300ms
Enter
Sleep 3s
Wait+Screen /(?m)^\s*Pronto para começar\.?\s*$/
Sleep 2s
Show
Sleep 500ms
Screenshot docs/assets/demo-sessao.png
Sleep 1s
Hide
Ctrl+C
Sleep 500ms
