package net.anzix.acpp.ui.setup

import androidx.lifecycle.ViewModel
import androidx.lifecycle.viewModelScope
import dagger.hilt.android.lifecycle.HiltViewModel
import net.anzix.acpp.data.remote.ConnectivityChecker
import net.anzix.acpp.data.settings.SettingsRepository
import net.anzix.acpp.data.settings.UrlNormalizer
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.launch
import javax.inject.Inject

data class SetupUiState(
    val url: String = "",
    val token: String = "",
    val connecting: Boolean = false,
    val error: String? = null,
)

@HiltViewModel
class SetupViewModel @Inject constructor(
    private val settings: SettingsRepository,
    private val checker: ConnectivityChecker,
) : ViewModel() {

    private val _state = MutableStateFlow(SetupUiState())
    val state: StateFlow<SetupUiState> = _state.asStateFlow()

    fun onUrlChange(value: String) = _state.update { it.copy(url = value, error = null) }

    fun onTokenChange(value: String) = _state.update { it.copy(token = value, error = null) }

    /** Validates and persists the config, invoking [onSuccess] when connected. */
    fun connect(onSuccess: () -> Unit) {
        val current = _state.value
        val normalized = UrlNormalizer.normalize(current.url)
        if (normalized == null) {
            _state.update { it.copy(error = "Enter a valid server URL") }
            return
        }
        _state.update { it.copy(connecting = true, error = null) }
        viewModelScope.launch {
            val result = checker.check(normalized, current.token.trim())
            result.fold(
                onSuccess = {
                    settings.save(normalized, current.token.trim())
                    _state.update { it.copy(connecting = false) }
                    onSuccess()
                },
                onFailure = { e ->
                    _state.update {
                        it.copy(connecting = false, error = e.message ?: "Connection failed")
                    }
                },
            )
        }
    }
}
