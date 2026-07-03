package net.anzix.acpp.ui.components

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.unit.dp

/** Prompts for a working directory to start a new session in. */
@Composable
fun NewSessionDialog(
    onDismiss: () -> Unit,
    onConfirm: (dir: String) -> Unit,
    initialDir: String = "",
) {
    var dir by remember { mutableStateOf(initialDir) }
    AlertDialog(
        onDismissRequest = onDismiss,
        title = { Text("New session") },
        text = {
            Column(verticalArrangement = Arrangement.spacedBy(8.dp)) {
                Text("Working directory on the server")
                OutlinedTextField(
                    value = dir,
                    onValueChange = { dir = it },
                    singleLine = true,
                    placeholder = { Text("/home/you/project") },
                    textStyle = androidx.compose.material3.MaterialTheme.typography.bodyMedium
                        .copy(fontFamily = FontFamily.Monospace),
                    modifier = Modifier
                        .fillMaxWidth()
                        .testTag("newsession_dir"),
                )
            }
        },
        confirmButton = {
            TextButton(
                onClick = { if (dir.isNotBlank()) onConfirm(dir.trim()) },
                modifier = Modifier.testTag("newsession_create"),
            ) { Text("Create") }
        },
        dismissButton = {
            TextButton(onClick = onDismiss) { Text("Cancel") }
        },
    )
}
