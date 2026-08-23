/*
 * Copyright (C) 2025 The Android Open Source Project
 * Copyright (C) 2026 The uwuAOSP Project
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *      http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package com.android.kotlin.compiler.client

import com.android.kotlin.compiler.cli.CacheMarker
import com.android.kotlin.compiler.cli.CacheMarkerError
import com.android.kotlin.compiler.cli.FileDeltaOptions
import com.android.kotlin.compiler.cli.HelpArgument
import com.android.kotlin.compiler.cli.parseArgs
import com.android.kotlin.compiler.snapshotter.fileToSnapshotFile
import java.io.File
import java.net.URLClassLoader
import java.util.UUID
import kotlin.io.path.ExperimentalPathApi
import kotlin.io.path.deleteRecursively
import kotlin.system.exitProcess
import org.jetbrains.kotlin.buildtools.api.CompilationResult
import org.jetbrains.kotlin.buildtools.api.CompilationService
import org.jetbrains.kotlin.buildtools.api.ExperimentalBuildToolsApi
import org.jetbrains.kotlin.buildtools.api.ProjectId
import org.jetbrains.kotlin.buildtools.api.SourcesChanges
import org.jetbrains.kotlin.buildtools.api.jvm.ClasspathSnapshotBasedIncrementalCompilationApproachParameters

private val ARGUMENT_PARSERS =
    listOf(
        BuildDirArgument(),
        BuildFileArgument(),
        BuildHistoryFileArgument(),
        ClassPathArgument(),
        Debug(),
        HelpArgument(),
        JvmArgument(),
        LogDirArgument(),
        OutputDirArgument(),
        PluginArgument(),
        RunFilesArgument(),
        RootDirArgument(),
        SrcJarsDirArgument(),
        SourceDeltaArgument(),
        Verbose(),
        WorkingDirArgument(),
        XBuildFileArgument(),
        SourcesArgument(), // must come last
    )

val USAGE_TEXT =
    """
        Usage: kotlin-incremental-client -root-dir=<dir> [options] [kotlinc options] [-- <source files>]
    """
        .trimIndent()

val ADDITIONAL_HELP =
    """
        EXAMPLES
        ========
        
        kotlin-incremental-client -root-dir=/tmp/helloworld -- HelloWorld.kt
        
        kotlin-incremental-client -root-dir=/tmp/helloworld -build-file=HelloWorldBuild.xml
        
        kotlin-incremental-client -root-dir=/tmp/helloworld -output-dir=out -- HelloWorld.kt
    """
        .trimIndent()

fun main(args: Array<String>) {
    val opts = ClientOptions()
    ARGUMENT_PARSERS.forEach { it.setupDefault(opts) }

    if (
        !parseArgs(
            args,
            opts,
            ARGUMENT_PARSERS,
            System.out,
            System.err,
            USAGE_TEXT,
            ADDITIONAL_HELP,
        )
    ) {
        exitProcess(-1)
    }

    if (opts.sources.isEmpty() && (opts.buildFile == null || opts.buildFileSources.isEmpty())) {
        println("No sources or build file specified. Exiting.")
        exitProcess(0)
    }

    val cacheMarker = CacheMarker(opts.workingDir)

    val result = btaCompilation(opts, cacheMarker)
    when (result) {
        CompilationResult.COMPILATION_SUCCESS -> {
            writeCacheMarker(cacheMarker)
            exitProcess(0)
        }
        CompilationResult.COMPILATION_ERROR -> {
            writeCacheMarker(cacheMarker)
            exitProcess(-1)
        }
        CompilationResult.COMPILATION_OOM_ERROR -> {
            // Assume the cache is invalid and don't write the marker.
            println("Out of Memory")
            exitProcess(-2)
        }

        CompilationResult.COMPILER_INTERNAL_ERROR -> {
            println("Internal compiler error. Please report to https://kotl.in/issue")
            exitProcess(-3)
        }
    }
}

fun writeCacheMarker(marker: CacheMarker) {
    if (!marker.write()) {
        println("Failed to write cache marker. Your next build will not be incremental.")
    }
}

@OptIn(ExperimentalPathApi::class)
fun btaCompilation(opts: ClientOptions, cacheMarker: CacheMarker): CompilationResult {
    val kotlincArgs = mutableListOf<String>()
    if (opts.buildFile != null) {
        if (opts.buildFileModuleName != null) {
            kotlincArgs.add("-module-name")
            kotlincArgs.add(opts.buildFileModuleName!!)
        }
        if (!opts.buildFileFriendDirs.isEmpty()) {
            kotlincArgs.add("-Xfriend-paths=" + opts.buildFileFriendDirs.joinToString(","))
        }
    }
    kotlincArgs.add("-d=${opts.outputDir.absolutePath}")
    kotlincArgs.addAll(opts.passThroughArgs)
    kotlincArgs.addAll(opts.sources)
    kotlincArgs.addAll(opts.buildFileJavaSources)

    if (!cacheMarker.isValid()) {
        println("Invalid or missing cache. Triggering full compile.")
        opts.workingDir.toPath().deleteRecursively()
        opts.outputDir.toPath().deleteRecursively()
    } else {
        if (!cacheMarker.remove()) {
            throw CacheMarkerError("Failed to remove cache marker. Aborting the build.")
        }
    }
    return doBtaCompilation(
        opts.sources + opts.buildFileSources,
        opts.classPath + opts.buildFileClassPaths,
        opts.workingDir,
        opts.outputDir,
        opts,
        kotlincArgs,
        opts.jvmArgs,
        Logger(opts.verbose, opts.debug),
    )
}

@OptIn(ExperimentalBuildToolsApi::class)
fun doBtaCompilation(
    sources: List<String>,
    classPath: List<String>,
    workingDirectory: File,
    outputDirectory: File,
    fileDeltaOptions: FileDeltaOptions,
    args: List<String>,
    jvmArgs: List<String>,
    logger: Logger,
): CompilationResult {
    var anyMissing = false
    sources.forEach {
        if (!File(it).exists()) {
            logger.error("Missing source: $it")
            anyMissing = true
        }
    }

    if (anyMissing) {
        return CompilationResult.COMPILATION_ERROR
    }

    val loader =
        URLClassLoader(
            classPath.map { File(it).toURI().toURL() }.toTypedArray() +
                // Need to include this code's own jar in the classpath.
                arrayOf(ClientOptions::class.java.protectionDomain?.codeSource?.location)
        )

    val service = CompilationService.loadImplementation(loader)
    val executionConfig = service.makeCompilerExecutionStrategyConfiguration()
    if (System.getenv("SOONG_KOTLIN_DAEMON")?.equals("true", ignoreCase = true) == true) {
        executionConfig.useDaemonStrategy(jvmArgs)
    } else {
        executionConfig.useInProcessStrategy()
    }
    val compilationConfig = service.makeJvmCompilationConfiguration()

    val cpsnapshotParameters = getClasspathSnapshotParameters(workingDirectory, classPath)

    val incJvmCompilationConfig =
        compilationConfig.makeClasspathSnapshotBasedIncrementalCompilationConfiguration()
    var sourceChanges: SourcesChanges = SourcesChanges.Unknown
    if (!outputDirectory.exists()) {
        incJvmCompilationConfig.forceNonIncrementalMode(true)
    } else {
        val modifiedFiles = fileDeltaOptions.modifiedFiles
        val removedFiles = fileDeltaOptions.removedFiles
        if (modifiedFiles != null && removedFiles != null) {
            sourceChanges = SourcesChanges.Known(modifiedFiles, removedFiles)

            // If we've changed 95% or more of the files, don't bother with incremental.
            // This may happen when a "version" change occurs in the tooling, meaning all input
            // files
            // are marked as changed and need to be recompiled.
            // Better to leave some wiggle room here, rather than comparing the sizes exactly,
            // in case of unforeseen edge cases.
            // Setting this value to true _does_ generate incremental cache data for the next run.
            if ((sourceChanges.modifiedFiles.size.toFloat() / sources.size.toFloat()) > 0.95) {
                incJvmCompilationConfig.forceNonIncrementalMode(true)
            }
        }
    }
    compilationConfig.useIncrementalCompilation(
        workingDirectory,
        sourceChanges,
        cpsnapshotParameters,
        incJvmCompilationConfig,
    )
    compilationConfig.useLogger(logger)

    val pid = ProjectId.ProjectUUID(UUID.randomUUID())
    val mArgs = args.toMutableList()
    mArgs.add("-cp")
    mArgs.add(classPath.joinToString(":"))
    return try {
        service.compileJvm(
            pid,
            executionConfig,
            compilationConfig,
            sources.map { File(it) },
            mArgs,
        )
    } finally {
        service.finishProjectCompilation(pid)
    }
}

@OptIn(ExperimentalBuildToolsApi::class)
fun getClasspathSnapshotParameters(
    workingDirectory: File,
    classPath: List<String>,
): ClasspathSnapshotBasedIncrementalCompilationApproachParameters {
    val cps = File(workingDirectory.parentFile, "shrunk-classpath-snapshot.bin")
    val cpsFiles =
        classPath.mapNotNull {
            val cpFile = File(it)
            if (!cpFile.exists()) {
                throw RuntimeException("classpath entry does not exist: $it")
            }

            val snf = fileToSnapshotFile(cpFile)

            if (!snf.exists()) {
                null
            } else {
                snf
            }
        }

    return ClasspathSnapshotBasedIncrementalCompilationApproachParameters(
        newClasspathSnapshotFiles = cpsFiles,
        shrunkClasspathSnapshot = cps,
    )
}
