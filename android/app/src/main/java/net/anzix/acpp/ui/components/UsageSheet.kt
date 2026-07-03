package net.anzix.acpp.ui.components

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.ModalBottomSheet
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.material3.rememberModalBottomSheetState
import androidx.compose.runtime.Composable
import androidx.compose.ui.Modifier
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.unit.dp
import net.anzix.acpp.domain.model.Conversation
import net.anzix.acpp.domain.model.Usage
import net.anzix.acpp.ui.util.RelativeTime
import net.anzix.acpp.ui.util.formatCost
import net.anzix.acpp.ui.util.formatTokens

@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun UsageSheet(
    conversation: Conversation?,
    usage: Usage,
    onDismiss: () -> Unit,
) {
    val sheetState = rememberModalBottomSheetState()
    ModalBottomSheet(onDismissRequest = onDismiss, sheetState = sheetState) {
        Column(
            modifier = Modifier
                .fillMaxWidth()
                .padding(start = 20.dp, end = 20.dp, bottom = 28.dp),
            verticalArrangement = Arrangement.spacedBy(12.dp),
        ) {
            Text("Usage", style = MaterialTheme.typography.titleLarge)

            Surface(
                color = MaterialTheme.colorScheme.primaryContainer,
                shape = MaterialTheme.shapes.large,
                modifier = Modifier.fillMaxWidth(),
            ) {
                Row(
                    modifier = Modifier.padding(16.dp),
                    horizontalArrangement = Arrangement.spacedBy(24.dp),
                ) {
                    SummaryStat(
                        label = "Context",
                        value = "${formatTokens(usage.contextUsed)} / ${formatTokens(usage.contextWindow)}",
                    )
                    val cost = formatCost(usage.costUsd ?: conversation?.costUsd)
                    if (cost.isNotBlank()) SummaryStat(label = "Cost", value = cost)
                }
            }

            HorizontalDivider()

            KeyValueRow("Model", conversation?.model.orEmpty(), mono = true)
            KeyValueRow("Status", conversation?.status?.name?.lowercase().orEmpty())
            KeyValueRow("Context used", formatTokens(usage.contextUsed), mono = true)
            KeyValueRow("Context window", formatTokens(usage.contextWindow), mono = true)
            conversation?.let {
                if (it.stopReason.isNotBlank()) KeyValueRow("Stop reason", it.stopReason)
                if (it.createdAt.isNotBlank()) KeyValueRow("Created", RelativeTime.format(it.createdAt) + " ago")
                if (it.updatedAt.isNotBlank()) KeyValueRow("Updated", RelativeTime.format(it.updatedAt) + " ago")
            }
        }
    }
}

@Composable
private fun SummaryStat(label: String, value: String) {
    Column(verticalArrangement = Arrangement.spacedBy(2.dp)) {
        Text(
            label,
            style = MaterialTheme.typography.labelMedium,
            color = MaterialTheme.colorScheme.onPrimaryContainer,
        )
        Text(
            value,
            style = MaterialTheme.typography.titleMedium,
            fontFamily = FontFamily.Monospace,
            color = MaterialTheme.colorScheme.onPrimaryContainer,
        )
    }
}

@Composable
private fun KeyValueRow(key: String, value: String, mono: Boolean = false) {
    if (value.isBlank()) return
    Row(modifier = Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.SpaceBetween) {
        Text(key, style = MaterialTheme.typography.bodyMedium, color = MaterialTheme.colorScheme.onSurfaceVariant)
        Text(
            value,
            style = MaterialTheme.typography.bodyMedium,
            fontFamily = if (mono) FontFamily.Monospace else FontFamily.Default,
            color = MaterialTheme.colorScheme.onSurface,
        )
    }
}
