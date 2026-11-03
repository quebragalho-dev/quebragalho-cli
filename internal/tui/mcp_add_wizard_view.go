package tui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/quebragalho-dev/quebragalho-cli/internal/redaction"
)

func (m model) mcpAddWizardOverlay(width int) string {
	if m.mcpAddWizard == nil {
		return ""
	}
	return m.mcpAddWizard.render(width)
}

func (wizard *mcpAddWizardState) render(width int) string {
	if wizard == nil {
		return ""
	}
	overlayWidth := mcpAddWizardOverlayWidth(width)
	innerWidth := maxInt(20, overlayWidth-4)
	lines := []string{
		qgTheme.faint.Render(mcpAddWizardStepLine(wizard.step)),
		qgTheme.line.Render(strings.Repeat("-", innerWidth)),
	}
	if wizard.err != "" {
		lines = append(lines, qgTheme.red.Render("erro: "+wizard.err), "")
	}
	switch wizard.step {
	case mcpAddWizardStepName:
		lines = append(lines, wizard.renderNameStep(innerWidth)...)
	case mcpAddWizardStepType:
		lines = append(lines, wizard.renderTypeStep(innerWidth)...)
	case mcpAddWizardStepEndpoint:
		lines = append(lines, wizard.renderEndpointStep(innerWidth)...)
	case mcpAddWizardStepHeader:
		lines = append(lines, wizard.renderHeaderStep(innerWidth)...)
	case mcpAddWizardStepConfirm:
		lines = append(lines, wizard.renderConfirmStep(innerWidth)...)
	case mcpAddWizardStepResult:
		lines = append(lines, wizard.renderResultStep(innerWidth)...)
	}
	lines = append(lines,
		qgTheme.line.Render(strings.Repeat("-", innerWidth)),
		qgTheme.faint.Render(wizard.footer()),
	)
	block := styledBlockFillTitle(overlayWidth, "Adicionar servidor MCP", lines, qgTheme.lineStrong, lipgloss.NewStyle())
	return centerRenderedBlock(block, width)
}

func (wizard *mcpAddWizardState) renderNameStep(width int) []string {
	value := displayValue(strings.TrimSpace(wizard.serverName), "digite um nome estável")
	return []string{
		qgTheme.accent.Render("Nome do servidor"),
		fitStyledLine(qgTheme.ink.Render("> "+value), width),
		qgTheme.faint.Render("Padrão: " + mcpAddWizardTypes[clampInt(wizard.selectedType, 0, len(mcpAddWizardTypes)-1)].Label),
		qgTheme.faint.Render("Use letras minúsculas, números, traços ou sublinhados."),
	}
}

func (wizard *mcpAddWizardState) renderTypeStep(width int) []string {
	lines := []string{qgTheme.accent.Render("Tipo de servidor")}
	for index, item := range mcpAddWizardTypes {
		marker := "  "
		surface := transparentSurface
		if index == wizard.selectedType {
			marker = "> "
			surface = qgTheme.onSel
		}
		line := marker + item.Label
		if item.Meta != "" {
			line += "  " + item.Meta
		}
		lines = append(lines, fillPaletteLine(surface(qgTheme.ink).Render(line), width, surface))
	}
	return lines
}

func (wizard *mcpAddWizardState) renderEndpointStep(width int) []string {
	title := "URL do endpoint"
	placeholder := "https://example.com/mcp"
	if !wizard.isRemote() {
		title = "Comando"
		placeholder = "npx -y @modelcontextprotocol/server-filesystem ."
	}
	value := displayValue(strings.TrimSpace(wizard.endpoint), placeholder)
	return []string{
		qgTheme.accent.Render(title),
		fitStyledLine(qgTheme.ink.Render("> "+value), width),
	}
}

func (wizard *mcpAddWizardState) renderHeaderStep(width int) []string {
	value := displayValue(strings.TrimSpace(wizard.headerInput), "pressione Enter para pular")
	return []string{
		qgTheme.accent.Render("Adicionar cabeçalho"),
		fitStyledLine(qgTheme.ink.Render("> "+redaction.RedactString(value, redaction.Options{})), width),
		qgTheme.faint.Render(`Cole "Chave: Valor" ou "Chave=Valor".`),
	}
}

func (wizard *mcpAddWizardState) renderConfirmStep(width int) []string {
	lines := []string{
		qgTheme.accent.Render("Revisar configuração"),
		"servidor: " + qgTheme.ink.Render(wizard.serverName),
		"tipo: " + qgTheme.ink.Render(strings.ToUpper(wizard.serverType)),
	}
	if source := strings.TrimSpace(wizard.sourceLabel); source != "" {
		lines = append(lines, "fonte: "+qgTheme.ink.Render(source))
	}
	if sourceURL := strings.TrimSpace(wizard.sourceURL); sourceURL != "" {
		lines = append(lines, "docs: "+qgTheme.ink.Render(redactMCPWizardDisplayValue(sourceURL)))
	}
	if wizard.isRemote() {
		lines = append(lines, "url: "+qgTheme.ink.Render(redactMCPWizardDisplayValue(wizard.endpoint)))
		if wizard.headerKey != "" {
			lines = append(lines, "cabeçalho: "+qgTheme.ink.Render(wizard.headerKey+"=[REDACTED]"))
		}
	} else {
		lines = append(lines, "comando: "+qgTheme.ink.Render(redactMCPWizardCommand(wizard.endpoint)))
	}
	if len(wizard.prerequisites) > 0 {
		lines = append(lines, "", qgTheme.accent.Render("Pré-requisitos"))
		for _, item := range wizard.prerequisites {
			lines = append(lines, "  - "+qgTheme.ink.Render(item))
		}
	}
	lines = append(lines, "", qgTheme.faint.Render("Enter salva e testa o servidor."))
	for index, line := range lines {
		lines[index] = fitStyledLine(line, width)
	}
	return lines
}

func (wizard *mcpAddWizardState) renderResultStep(width int) []string {
	result := wizard.result
	title := result.Title
	if title == "" {
		title = "Problema de configuração do MCP"
	}
	state := displayValue(result.State, "não salvo")
	transport := "HTTP remoto"
	if !wizard.isRemote() {
		transport = "Stdio local"
	} else if wizard.serverType == "sse" {
		transport = "SSE remoto"
	}
	lines := []string{
		qgTheme.accent.Render(title),
		qgTheme.ink.Bold(true).Render(displayValue(wizard.serverName, "sem nome")) + "  " + qgTheme.faint.Render(state),
		qgTheme.faint.Render(transport),
	}
	if result.Message != "" {
		lines = append(lines, fitStyledLine(qgTheme.red.Render(result.Message), width))
	}
	if !result.Saved {
		lines = append(lines, qgTheme.faint.Render("Nenhuma configuração foi salva ainda."))
	} else {
		lines = append(lines, fmt.Sprintf("Ferramentas encontradas: %d", maxInt(0, result.ToolCount)))
	}
	lines = append(lines, "")
	for index, action := range wizard.resultActionLabels() {
		marker := "  "
		if index == clampInt(wizard.resultSelected, 0, len(wizard.resultActionLabels())-1) {
			marker = "> "
		}
		lines = append(lines, marker+action)
	}
	for index, line := range lines {
		lines[index] = fitStyledLine(line, width)
	}
	return lines
}

func redactMCPWizardDisplayValue(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if looksLikeMCPDisplayURLValue(value) {
		return redactMCPDisplayURL(value)
	}
	return redaction.RedactString(value, redaction.Options{})
}

func redactMCPWizardCommand(value string) string {
	parts, err := splitMCPCommandArgs(value)
	if err != nil || len(parts) == 0 {
		return redactMCPWizardDisplayValue(value)
	}
	redacted := make([]string, 0, len(parts))
	head := strings.TrimSpace(parts[0])
	if looksLikeMCPDisplayURLValue(head) {
		head = redactMCPDisplayURL(head)
	} else {
		head = redaction.RedactString(head, redaction.Options{})
	}
	redacted = append(redacted, head)
	redacted = append(redacted, redactedCommandArgs(parts[1:])...)
	return strings.Join(redacted, " ")
}

func (wizard *mcpAddWizardState) resultActionLabels() []string {
	switch {
	case wizard == nil:
		return nil
	case wizard.result.Connected:
		return []string{"Usar servidor", "Gerenciar ferramentas", "Editar configuração", "Desativar servidor"}
	case wizard.result.Saved:
		return []string{"Tentar conectar de novo", "Editar configuração", "Desativar servidor", "Remover servidor"}
	default:
		return []string{"Editar URL", "Salvar desativado", "Descartar"}
	}
}

func (wizard *mcpAddWizardState) footer() string {
	switch wizard.step {
	case mcpAddWizardStepType:
		return "↑/↓ escolher   Enter continuar   Esc fechar"
	case mcpAddWizardStepResult:
		return "Enter escolher   r tentar de novo   s salvar desativado   d descartar"
	default:
		return "Enter continuar   ← voltar   Esc fechar"
	}
}

func mcpAddWizardOverlayWidth(width int) int {
	if width <= 0 {
		return mcpAddWizardMaxWidth
	}
	target := minInt(width, mcpAddWizardMaxWidth)
	if target < mcpAddWizardMinWidth {
		return width
	}
	return target
}

func mcpAddWizardStepLine(step mcpAddWizardStep) string {
	steps := []struct {
		step  mcpAddWizardStep
		label string
	}{
		{mcpAddWizardStepName, "1 nome"},
		{mcpAddWizardStepType, "2 tipo"},
		{mcpAddWizardStepEndpoint, "3 endpoint"},
		{mcpAddWizardStepHeader, "4 autenticação"},
		{mcpAddWizardStepConfirm, "5 confirmar"},
	}
	parts := make([]string, 0, len(steps))
	for _, item := range steps {
		if item.step == step {
			parts = append(parts, "["+item.label+"]")
		} else {
			parts = append(parts, item.label)
		}
	}
	return strings.Join(parts, "  ")
}
