import java.io.ByteArrayInputStream;
import java.io.OutputStream;
import java.io.PrintStream;
import java.nio.charset.StandardCharsets;
import java.nio.file.Path;
import java.util.Arrays;
import org.neo4j.cli.ExecutionContext;
import org.neo4j.commandline.admin.security.SetInitialPasswordCommand;
import org.neo4j.io.fs.DefaultFileSystemAbstraction;
import picocli.CommandLine;

/** Pinned Neo4j 2026.10.1 tooling; credential never enters OS argv or output. */
public final class Neo4jInitialPassword {
    public static void main(String[] args) {
        if (args.length != 2) System.exit(1);
        byte[] secret = null;
        try (PrintStream quiet = new PrintStream(OutputStream.nullOutputStream());
             DefaultFileSystemAbstraction fs = new DefaultFileSystemAbstraction()) {
            secret = System.in.readNBytes(4097);
            if (secret.length < 8 || secret.length > 4096) System.exit(1);
            String password = new String(secret, StandardCharsets.UTF_8);
            if (password.indexOf('\n') >= 0 || password.indexOf('\r') >= 0) System.exit(1);
            ExecutionContext context = new ExecutionContext(Path.of(args[0]), Path.of(args[1]),
                quiet, quiet, new ByteArrayInputStream(new byte[0]), fs);
            int result = new CommandLine(new SetInitialPasswordCommand(context)).execute(password);
            Arrays.fill(secret, (byte) 0);
            if (result != 0) System.exit(1);
        } catch (Exception failure) {
            if (secret != null) Arrays.fill(secret, (byte) 0);
            System.exit(1);
        }
    }
}
