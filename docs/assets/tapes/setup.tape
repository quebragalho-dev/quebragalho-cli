Env QG_CRED_STORAGE "file"
Env QG_NO_UPDATE_CHECK "1"
Env QG_NO_BROWSER "1"
Hide
Type `test "$QG_DEMO_ISOLATED" = 1 || exit 1; mkdir -p "$HOME/meu-projeto" "$XDG_CONFIG_HOME/qg"`
Sleep 300ms
Enter
Type `test -f "$XDG_CONFIG_HOME/qg/config.json" || printf %s '{"activeProvider": "qg", "providers": [{"name": "qg", "catalogID": "qg", "provider_kind": "openai-compatible", "baseURL": "https://api.quebragalho.dev/v1", "apiKeyEnv": "QG_API_KEY", "model": "muse-spark-1.3-contributor"}], "preferences": {"theme": "system", "recaps": false}}' > "$XDG_CONFIG_HOME/qg/config.json"`
Sleep 300ms
Enter
Type `printf %b '# Lista de tarefas\n\nUm projeto pequeno em Python para organizar tarefas.\n\n- tarefas.py guarda a lista e marca tarefas como concluídas.\n- test_tarefas.py testa o comportamento.\n\nExecute os testes com python3 -m unittest -v.\n' > "$HOME/meu-projeto/README.md"`
Sleep 300ms
Enter
Type `printf %b 'class ListaDeTarefas:\n    def __init__(self):\n        self.tarefas = []\n\n    def adicionar(self, titulo):\n        self.tarefas.append({"titulo": titulo, "concluida": False})\n\n    def concluir(self, indice):\n        self.tarefas[indice]["concluida"] = True\n' > "$HOME/meu-projeto/tarefas.py"`
Sleep 300ms
Enter
Type `printf %b 'import unittest\nfrom tarefas import ListaDeTarefas\n\nclass TestListaDeTarefas(unittest.TestCase):\n    def test_adicionar_e_concluir(self):\n        lista = ListaDeTarefas()\n        lista.adicionar("Preparar o café")\n        self.assertFalse(lista.tarefas[0]["concluida"])\n        lista.concluir(0)\n        self.assertTrue(lista.tarefas[0]["concluida"])\n' > "$HOME/meu-projeto/test_tarefas.py"`
Sleep 300ms
Enter
Type `cd "$HOME/meu-projeto"; clear`
Sleep 300ms
Enter
Sleep 300ms
