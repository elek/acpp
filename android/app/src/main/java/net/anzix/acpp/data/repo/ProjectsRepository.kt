package net.anzix.acpp.data.repo

import net.anzix.acpp.data.remote.AcppApi
import net.anzix.acpp.domain.model.Project
import javax.inject.Inject
import javax.inject.Singleton

@Singleton
class ProjectsRepository @Inject constructor(
    private val api: AcppApi,
) {
    suspend fun projects(): List<Project> = api.projects().map { it.toDomain() }
}
