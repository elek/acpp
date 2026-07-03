package net.anzix.acpp.ui.components

import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.ui.Modifier
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.unit.dp

private data class SlashCommand(val name: String, val description: String)

private val COMMANDS = listOf(
    SlashCommand("/clear", "Start a fresh session"),
    SlashCommand("/cancel", "Stop the current run"),
    SlashCommand("/compact", "Compact the context"),
    SlashCommand("/cost", "Show session cost"),
    SlashCommand("/diff", "Show pending changes"),
    SlashCommand("/model", "Switch model"),
)

/**
 * Slash-command suggestions shown above the composer while the draft is a partial
 * command (starts with "/" and has no space yet). Hidden otherwise.
 */
@Composable
fun SlashPalette(draft: String, onPick: (String) -> Unit, modifier: Modifier = Modifier) {
    if (!draft.startsWith("/") || draft.contains(' ')) return
    val matches = COMMANDS.filter { it.name.startsWith(draft, ignoreCase = true) }
    if (matches.isEmpty()) return

    Surface(
        color = MaterialTheme.colorScheme.surfaceContainerHigh,
        shape = MaterialTheme.shapes.medium,
        modifier = modifier
            .fillMaxWidth()
            .padding(horizontal = 12.dp, vertical = 4.dp),
    ) {
        Column {
            matches.forEach { cmd ->
                Row(
                    modifier = Modifier
                        .fillMaxWidth()
                        .clickable { onPick(cmd.name) }
                        .padding(horizontal = 14.dp, vertical = 10.dp),
                ) {
                    Text(
                        cmd.name,
                        style = MaterialTheme.typography.labelLarge,
                        fontFamily = FontFamily.Monospace,
                        color = MaterialTheme.colorScheme.primary,
                        modifier = Modifier.padding(end = 12.dp),
                    )
                    Text(
                        cmd.description,
                        style = MaterialTheme.typography.bodySmall,
                        color = MaterialTheme.colorScheme.onSurfaceVariant,
                    )
                }
            }
        }
    }
}
