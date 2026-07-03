package net.anzix.acpp.ui.projects

import androidx.lifecycle.ViewModel
import androidx.lifecycle.viewModelScope
import dagger.hilt.android.lifecycle.HiltViewModel
import net.anzix.acpp.data.repo.ProjectsRepository
import net.anzix.acpp.data.repo.SessionsRepository
import net.anzix.acpp.domain.model.Project
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.launch
import javax.inject.Inject

data class ProjectsUiState(
    val loading: Boolean = true,
    val error: String? = null,
    val projects: List<Project> = emptyList(),
)

@HiltViewModel
class ProjectsViewModel @Inject constructor(
    private val projectsRepo: ProjectsRepository,
    private val sessionsRepo: SessionsRepository,
) : ViewModel() {

    private val _state = MutableStateFlow(ProjectsUiState())
    val state: StateFlow<ProjectsUiState> = _state.asStateFlow()

    init {
        load()
    }

    fun load() {
        _state.update { it.copy(loading = true, error = null) }
        viewModelScope.launch {
            runCatching { projectsRepo.projects() }.fold(
                onSuccess = { ps -> _state.update { it.copy(loading = false, projects = ps) } },
                onFailure = { e -> _state.update { it.copy(loading = false, error = e.message ?: "Failed to load projects") } },
            )
        }
    }

    /** Creates a new session for [dir] and navigates to it on success. */
    fun createSession(dir: String, onCreated: (String) -> Unit) {
        viewModelScope.launch {
            runCatching { sessionsRepo.createSession(dir) }.fold(
                onSuccess = { id -> onCreated(id) },
                onFailure = { e -> _state.update { it.copy(error = e.message ?: "Failed to create session") } },
            )
        }
    }
}
