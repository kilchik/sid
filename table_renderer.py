#!/usr/bin/env python

import os
import plotly.plotly as py
import plotly.graph_objs as go
import pandas as pd
import sys


def write_error(msg):
    sys.stderr.write("[E] %s\n" % msg)


def main():
    if len(sys.argv) != 2:
        write_error("invalid number of arguments")
        return 1

    csv_path = sys.argv[1]
    if not os.path.exists(csv_path):
        write_error("no such file: %s" % csv_path)
        return 1

    # expected filename is "Name-Timestamp.csv"
    name, _ = os.path.splitext(os.path.basename(csv_path))

    try:
        py.sign_in("DemoAccount", "2qdyfjyr7o")
        df = pd.read_csv(csv_path)

        table_height = 0
        for row_id in range(df.size/4-1):
            chars_num = len(str(df.iat[row_id,0]))
            cell_height = 0.893 * chars_num + 33.465
            table_height += cell_height

        trace = go.Table(
            header=dict(values=df.columns,
                        fill = dict(color='#C2D4FF'),
                        align = ['left'] * 5),
            cells=dict(values=[df.Title, df.Amount, df.Payer, df.Date],
                       fill = dict(color='#F5F8FF'),
                       align = ['left'] * 5))

        data = [trace]
        py.image.save_as(data, filename="%s.png" % name, scale=3, height=table_height)

    except Exception as e:
        write_error("caugth exception: %s" % e.message)
        return 1


if __name__ == "__main__":
    sys.exit(main())
