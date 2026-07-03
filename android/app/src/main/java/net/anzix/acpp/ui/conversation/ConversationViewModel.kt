package net.anzix.acpp.ui.conversation

import androidx.lifecycle.SavedStateHandle
import androidx.lifecycle.ViewModel
import androidx.lifecycle.viewModelScope
import androidx.navigation.toRoute
import dagger.hilt.android.lifecycle.HiltViewModel
import net.anzix.acpp.data.remote.EventReducer
import net.anzix.acpp.data.remote.SocketEvent
import net.anzix.acpp.data.repo.ConversationRepository
import net.anzix.acpp.domain.model.Conversation
import net.anzix.acpp.domain.model.ConversationState
import net.anzix.acpp.ui.nav.Conversation as ConversationRoute
import kotlinx.coroutines.Job
import kotlinx.coroutines.flow.MutableSharedFlow
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.SharedFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asSharedFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.launch
import javax.inject.Inject

data class ConversationUiState(
    val loading: Boolean = true,
    val error: String? = null,
    val conversation: Conversation? = null,
    val state: ConversationState = ConversationState(),
    val connected: Boolean = false,
)

@HiltViewModel
class ConversationViewModel @Inject constructor(
    savedStateHandle: SavedStateHandle,
    private val repo: ConversationRepository,
    private val reducer: EventReducer,
) : ViewModel() {

    val sessionId: String = savedStateHandle.toRoute<ConversationRoute>().sessionId

    private val _ui = MutableStateFlow(ConversationUiState())
    val ui: StateFlow<ConversationUiState> = _ui.asStateFlow()

    // One-shot navigation to a replacement session (after /clear).
    private val _nav = MutableSharedFlow<String>(extraBufferCapacity = 1)
    val navEvents: SharedFlow<String> = _nav.asSharedFlow()

    private var socketJob: Job? = null

    init {
        load()
        connect()
    }

    fun load() {
        _ui.update { it.copy(loading = it.conversation == null, error = null) }
        viewModelScope.launch {
            runCatching {
                val snapshot = repo.snapshot(sessionId)
                val history = repo.history(sessionId)
                snapshot to reducer.replay(history)
            }.fold(
                onSuccess = { (snap, state) ->
                    _ui.update { it.copy(loading = false, conversation = snap, state = state) }
                },
                onFailure = { e ->
                    _ui.update { it.copy(loading = false, error = e.message ?: "Failed to load conversation") }
                },
            )
        }
    }

    private fun connect() {
        socketJob?.cancel()
        socketJob = viewModelScope.launch {
            repo.events(sessionId).collect { ev ->
                when (ev) {
                    is SocketEvent.Connected -> {
                        _ui.update { it.copy(connected = true) }
                        // Re-replay persisted history on every (re)connection to resync.
                        runCatching { repo.history(sessionId) }.onSuccess { h ->
                            _ui.update { it.copy(state = reducer.replay(h)) }
                        }
                    }

                    is SocketEvent.Entry -> {
                        applyState(reducer.reduce(_ui.value.state, ev.entry))
                    }

                    is SocketEvent.Failure -> _ui.update { it.copy(connected = false) }
                }
            }
        }
    }

    private fun applyState(newState: ConversationState) {
        val nav = newState.navigateTo
        if (nav != null) {
            _nav.tryEmit(nav)
            _ui.update { it.copy(state = newState.copy(navigateTo = null)) }
        } else {
            _ui.update { it.copy(state = newState) }
        }
    }

    /** Sends a prompt (or slash command). Optimistically marks running; the WS echo is the truth. */
    fun send(text: String) {
        val t = text.trim()
        if (t.isEmpty()) return
        _ui.update { it.copy(state = it.state.copy(running = true)) }
        viewModelScope.launch {
            runCatching { repo.sendPrompt(sessionId, t) }
                .onFailure { e -> _ui.update { it.copy(error = e.message ?: "Failed to send") } }
        }
    }

    fun cancel() {
        viewModelScope.launch {
            runCatching { repo.stop(sessionId) }
                .onFailure { e -> _ui.update { it.copy(error = e.message ?: "Failed to cancel") } }
        }
    }

    fun clearError() = _ui.update { it.copy(error = null) }
}
