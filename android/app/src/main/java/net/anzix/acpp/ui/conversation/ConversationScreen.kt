package net.anzix.acpp.ui.conversation

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.itemsIndexed
import androidx.compose.foundation.lazy.rememberLazyListState
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.ArrowBack
import androidx.compose.material.icons.filled.MoreVert
import androidx.compose.material3.DropdownMenu
import androidx.compose.material3.DropdownMenuItem
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Scaffold
import androidx.compose.material3.SnackbarHost
import androidx.compose.material3.SnackbarHostState
import androidx.compose.material3.Text
import androidx.compose.material3.TopAppBar
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.hilt.navigation.compose.hiltViewModel
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import net.anzix.acpp.ui.components.ErrorBanner
import net.anzix.acpp.ui.components.LoadingBox
import net.anzix.acpp.ui.components.ReconnectingBanner
import net.anzix.acpp.ui.components.SlashPalette
import net.anzix.acpp.ui.components.UsageSheet
import net.anzix.acpp.ui.conversation.components.Composer
import net.anzix.acpp.ui.conversation.components.MessageItem
import net.anzix.acpp.ui.conversation.components.RunningBanner
import net.anzix.acpp.ui.conversation.components.TelemetryBar
import kotlinx.coroutines.launch

@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun ConversationScreen(
    onBack: () -> Unit,
    onSessionReplaced: (String) -> Unit,
    onSwitchProject: () -> Unit,
    viewModel: ConversationViewModel = hiltViewModel(),
) {
    val ui by viewModel.ui.collectAsStateWithLifecycle()
    var draft by remember { mutableStateOf("") }
    var menuOpen by remember { mutableStateOf(false) }
    var showUsage by remember { mutableStateOf(false) }
    val listState = rememberLazyListState()
    val snackbarHostState = remember { SnackbarHostState() }
    val scope = rememberCoroutineScope()

    fun toast(message: String) = scope.launch { snackbarHostState.showSnackbar(message) }

    LaunchedEffect(Unit) {
        viewModel.navEvents.collect { newId -> onSessionReplaced(newId) }
    }

    LaunchedEffect(ui.state.messages.size) {
        if (ui.state.messages.isNotEmpty()) {
            listState.animateScrollToItem(ui.state.messages.lastIndex)
        }
    }

    Scaffold(
        snackbarHost = { SnackbarHost(snackbarHostState) },
        topBar = {
            Column {
                TopAppBar(
                    navigationIcon = {
                        IconButton(onClick = onBack) {
                            Icon(Icons.AutoMirrored.Filled.ArrowBack, contentDescription = "Back")
                        }
                    },
                    title = {
                        Column {
                            Text(
                                ui.conversation?.title?.ifBlank { "Conversation" } ?: "Conversation",
                                style = MaterialTheme.typography.titleMedium,
                                maxLines = 1,
                                overflow = TextOverflow.Ellipsis,
                            )
                            val model = ui.conversation?.model
                            if (!model.isNullOrBlank()) {
                                Text(
                                    model,
                                    style = MaterialTheme.typography.bodySmall,
                                    fontFamily = FontFamily.Monospace,
                                    color = MaterialTheme.colorScheme.onSurfaceVariant,
                                    maxLines = 1,
                                )
                            }
                        }
                    },
                    actions = {
                        IconButton(
                            onClick = { menuOpen = true },
                            modifier = Modifier.testTag("conversation_menu"),
                        ) {
                            Icon(Icons.Filled.MoreVert, contentDescription = "More")
                        }
                        DropdownMenu(expanded = menuOpen, onDismissRequest = { menuOpen = false }) {
                            DropdownMenuItem(
                                modifier = Modifier.testTag("menu_clear"),
                                text = { Text("Clear") },
                                onClick = {
                                    menuOpen = false
                                    viewModel.send("/clear")
                                    toast("Context cleared")
                                },
                            )
                            DropdownMenuItem(
                                text = { Text("Cancel run") },
                                onClick = {
                                    menuOpen = false
                                    viewModel.cancel()
                                    toast("Run cancelled")
                                },
                            )
                            DropdownMenuItem(
                                text = { Text("Switch project") },
                                onClick = { menuOpen = false; onSwitchProject() },
                            )
                            DropdownMenuItem(
                                text = { Text("Pin") },
                                onClick = { menuOpen = false; toast("Pinned") },
                            )
                        }
                    },
                )
                if (!ui.connected && !ui.loading) {
                    ReconnectingBanner()
                }
                TelemetryBar(
                    usage = ui.state.usage,
                    model = ui.conversation?.model ?: "",
                    onClick = { showUsage = true },
                )
            }
        },
        bottomBar = {
            Column {
                ui.error?.let { err ->
                    ErrorBanner(
                        message = err,
                        onRetry = viewModel::load,
                        onDismiss = viewModel::clearError,
                    )
                }
                if (ui.state.running) {
                    RunningBanner(onCancel = viewModel::cancel)
                }
                SlashPalette(
                    draft = draft,
                    onPick = { cmd -> draft = "$cmd " },
                )
                Composer(
                    draft = draft,
                    onDraftChange = { draft = it },
                    onSend = {
                        viewModel.send(draft)
                        draft = ""
                    },
                    onSlash = { if (draft.isEmpty()) draft = "/" },
                )
            }
        },
    ) { padding ->
        when {
            ui.loading -> LoadingBox(Modifier.padding(padding))
            ui.error != null && ui.state.messages.isEmpty() ->
                net.anzix.acpp.ui.components.ErrorBox(
                    ui.error!!,
                    onRetry = viewModel::load,
                    modifier = Modifier.padding(padding),
                )

            else -> LazyColumn(
                state = listState,
                modifier = Modifier.fillMaxSize(),
                contentPadding = PaddingValues(
                    start = 12.dp, end = 12.dp,
                    top = padding.calculateTopPadding() + 8.dp,
                    bottom = padding.calculateBottomPadding() + 8.dp,
                ),
                verticalArrangement = Arrangement.spacedBy(12.dp),
            ) {
                itemsIndexed(ui.state.messages, key = { i, _ -> i }) { _, message ->
                    MessageItem(message)
                }
            }
        }
    }

    if (showUsage) {
        UsageSheet(
            conversation = ui.conversation,
            usage = ui.state.usage,
            onDismiss = { showUsage = false },
        )
    }
}
