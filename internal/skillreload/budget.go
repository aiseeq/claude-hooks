package skillreload

// truncatedMarker — пометка Claude Code в конце копии скилла, урезанной
// сжатием (2.1.308, qBn)
const truncatedMarker = "[... skill content truncated for compaction; use Read on the skill path if you need the full text]"

// outputLimit — сколько символов additionalContext хука Claude Code кладёт в
// контекст: длиннее уходит в файл tool-results, агенту остаётся превью на
// 2 КБ (2.1.308: Tge, порог по умолчанию Y7s=1e4, длина строки JS)
const outputLimit = 10000

// outputBudget — предел выдачи одного события с запасом под outputLimit
var outputBudget = 9000
