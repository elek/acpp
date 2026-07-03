package net.anzix.acpp.ui.conversations

import androidx.lifecycle.SavedStateHandle
import androidx.lifecycle.ViewModel
import androidx.lifecycle.viewModelScope
import androidx.navigation.toRoute
import dagger.hilt.android.lifecycle.HiltViewModel
import net.anzix.acpp.data.repo.ProjectsRepository
import net.anzix.acpp.data.repo.SessionsRepository
import net.anzix.acpp.domain.model.Conversation
import net.anzix.acpp.domain.model.Project
import net.anzix.acpp.ui.nav.Conversations
import kotlinx.coroutines.async
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.launch
import javax.inject.Inject

data class ConversationsUiState(
    val loading: Boolean = true,
    val error: String? = null,
    val project: Project? = null,
    val sessions: List<Conversation> = emptyList(),
)

@HiltViewModel
class ConversationsViewModel @Inject constructor(
    savedStateHandle: SavedStateHandle,
    private val sessionsRepo: SessionsRepository,
    private val projectsRepo: ProjectsRepository,
) : ViewModel() {

    val projectName: String = savedStateHandle.toRoute<Conversations>().projectName

    private val _state = MutableStateFlow(ConversationsUiState())
    val state: StateFlow<ConversationsUiState> = _state.asStateFlow()

    init {
        load()
    }

    fun load() {
        _state.update { it.copy(loading = true, error = null) }
        viewModelScope.launch {
            runCatching {
                val sessionsDeferred = async { sessionsRepo.sessions(projectName) }
                val project = runCatching {
                    projectsRepo.projects().firstOrNull { it.name == projectName }
                }.getOrNull()
                project to sessionsDeferred.await()
            }.fold(
                onSuccess = { (project, sessions) ->
                    _state.update { it.copy(loading = false, project = project, sessions = sessions) }
                },
                onFailure = { e ->
                    _state.update { it.copy(loading = false, error = e.message ?: "Failed to load conversations") }
                },
            )
        }
    }

    /** Creates a new chat in this project's directory. */
    fun newChat(onCreated: (String) -> Unit) {
        val dir = _state.value.project?.dir
        if (dir.isNullOrBlank()) {
            _state.update { it.copy(error = "Project directory unknown") }
            return
        }
        viewModelScope.launch {
            runCatching { sessionsRepo.createSession(dir) }.fold(
                onSuccess = { id -> onCreated(id) },
                onFailure = { e -> _state.update { it.copy(error = e.message ?: "Failed to create chat") } },
            )
        }
    }
}
