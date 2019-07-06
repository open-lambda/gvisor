#include <assert.h>
#include <stdio.h>
#include <stdlib.h>
#include <unistd.h>
#include <string.h>
#include <sys/stat.h>
#include <sys/time.h>
#include <sys/types.h>
#include <time.h>

#define LAYERS 10
#define FILES_EACH_LAYER 2000
#define MIN_SIZE 1000
#define MAX_SIZE 2000
#define REPEAT 30

char path[128];

double get_wall_time(){
    struct timeval time;
    if (gettimeofday(&time,NULL)){
        printf("ERROR when fetching current wall time.\n");
		exit(1);
        return 0;
    }
    return (double)time.tv_sec + (double)time.tv_usec * .000001;
}

double test_layer(int layer) {
  struct stat st;
  double before = get_wall_time();
  for (int j = 0; j < FILES_EACH_LAYER; j++) {
     sprintf(path, "./%d-%d.txt", layer, j);
     int rc = stat(path, &st);
     assert(rc == 0);
  }
  return get_wall_time() - before;
}

void print_layers_time_cost(double msec[], double sum) {
  int i;
  for (int i = 0; i < LAYERS; i++) {
    printf("Layer %d: %f s\n", i, msec[i]);
  }
  printf("\nTotal: %f s (%d stat tests).\n", sum, LAYERS * FILES_EACH_LAYER);
}

double test_layers(double msec[]) {
  double sum = 0;
  for (int i = 0; i < LAYERS; i++) {
    msec[i] = test_layer(i);
    sum += msec[i];
  }
  return sum;
}

double test_layers_rev(double msec[]) {
  double sum = 0;
  for (int i = LAYERS - 1; i >= 0; i--) {
    msec[i] = test_layer(i);
    sum += msec[i];
  }
  return sum;
}

int get_repeat_times(int argc, char *argv[]) {
  printf("repeat mode\n");
  if (argc >= 3) {
    int rt = atoi(argv[2]);
    printf("the default repeat times has been changed to %d\n", rt);
    return rt;
  }
  else {
    return REPEAT;
  }
}

int main(int argc, char *argv[]) {
    int i, j;
    struct stat st;
    double msec[LAYERS];
    double sum = 0;

    double total_msec[LAYERS];
    double total_sum = 0;

    int repeat_times = REPEAT;

    srand(time(0));

    if (strcmp(argv[1], "new") == 0) {
        printf("creating new test files.\n");
        char debug = 0;
        if (argc >= 3 && strcmp(argv[2], "debug") == 0) {
          debug = 1;
        }
        for (i = 0; i < LAYERS; i++) {
            sprintf(path, "./layer-%d", i);
            if (stat(path, &st) == -1) {
                mkdir(path, 0777);
            }
        }

        char random_data[MAX_SIZE + 1];
        for (i = 0; i < LAYERS; i++) {
            for (j = 0; j < FILES_EACH_LAYER; j++) {
                if (debug) {
                  sprintf(path, "./%d-%d.txt", i, j);
                }
                else {
                  sprintf(path, "./layer-%d/%d-%d.txt", i, i, j);
                }
                //printf("file: %s\n", path);
                FILE *fp;
                fp = fopen(path, "w");
                if (fp == NULL) {
                    printf("ERROR. CANNOT OPEN THE FILE: %s\n", path);
                    exit(1);
                }
                else {
                    int sz = rand() % (MAX_SIZE - MIN_SIZE + 1) + MIN_SIZE;
                    int k = 0;
                    while (k < sz) {
                        random_data[k++] = rand() % 127;
                    }
                    random_data[sz] = 0;
                    fprintf(fp, "%s", random_data);
                }
                fclose(fp);
            }
        }
    }
    else if (strcmp(argv[1], "test") == 0) {
      sum = test_layers(msec);
      print_layers_time_cost(msec, sum);
    }
    else if (strcmp(argv[1], "test_rev") == 0) {
      sum = test_layers_rev(msec);
      print_layers_time_cost(msec, sum);
    }
    else if (strcmp(argv[1], "test_repeat") == 0) {
      repeat_times = get_repeat_times(argc, argv);
      memset(total_msec, 0, sizeof(total_msec));
      for (i = 0; i < repeat_times; i++) {
        test_layers(msec);
        for (j = 0; j < LAYERS; j++) {
          total_msec[j] += msec[j];
        }
      }
      for (j = 0; j < LAYERS; j++) {
        total_sum += total_msec[j];
      }
      print_layers_time_cost(total_msec, total_sum);
    }
    else if (strcmp(argv[1], "test_repeat_rev") == 0) {
      repeat_times = get_repeat_times(argc, argv);
      memset(total_msec, 0, sizeof(total_msec));
      for (i = 0; i < repeat_times; i++) {
        test_layers(msec);
        for (j = LAYERS - 1; j >= 0; j--) {
          total_msec[j] += msec[j];
        }
      }
      for (j = 0; j < LAYERS; j++) {
        total_sum += total_msec[j];
      }
      print_layers_time_cost(total_msec, total_sum);
    }
    else if (strcmp(argv[1], "dockerfile") == 0) {
      printf("ADD test /root\n");
      for (i = LAYERS - 1; i >= 0; i--) {
        printf("ADD layer-%d /root\n", i);
      }
    }
}
